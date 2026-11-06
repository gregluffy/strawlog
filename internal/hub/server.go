// Package hub serves the dashboard and API, and fans requests out to nodes
// (the in-process local node and remote agents over pinned mTLS).
package hub

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"strawlog/internal/agent"
	"strawlog/internal/api"
	"strawlog/internal/tlsutil"
)

//go:embed all:dist
var distFS embed.FS

const LocalID = "local"

type Config struct {
	Listen         string
	DataDir        string
	Local          api.Node // nil when the hub runs without a local node
	LocalName      string
	AdminUser      string // bootstrap admin username (default "admin")
	AdminPassword  string // optional; creates the first admin when there are no users yet
	TLS            string // "off", "auto", or "cert.pem:key.pem"
	PublicURL      string // optional dashboard URL used in notifications
	NotifyInterval time.Duration
	Log            *slog.Logger
}

type Server struct {
	cfg        Config
	log        *slog.Logger
	store      *store
	sess       *sessions
	loginLim   *limiter
	userLim    *limiter
	testLim    *limiter
	client     tls.Certificate
	setupToken string
	secure     bool

	notify *notifier
	events chan Event
	failed failedLogins

	mu      sync.RWMutex
	remotes map[string]*Remote

	follows atomic.Int32
}

func New(cfg Config) (*Server, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	st, err := openStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	cl, err := tlsutil.LoadOrCreate(filepath.Join(cfg.DataDir, "hub-client.crt"), filepath.Join(cfg.DataDir, "hub-client.key"), "strawlog-hub-"+host)
	if err != nil {
		return nil, err
	}
	if cfg.AdminUser == "" {
		cfg.AdminUser = "admin"
	}
	if cfg.NotifyInterval <= 0 {
		cfg.NotifyInterval = 5 * time.Minute
	}
	s := &Server{cfg: cfg, log: cfg.Log, store: st, sess: newSessions(), client: cl, remotes: map[string]*Remote{},
		loginLim: newLimiter(10, 5*time.Minute), userLim: newLimiter(10, 15*time.Minute), testLim: newLimiter(6, time.Minute),
		notify: newNotifier(strings.TrimRight(cfg.PublicURL, "/")), events: make(chan Event, 64)}
	if err := st.update(func(h *hubState) error {
		if migrateLegacy(h) {
			s.log.Info("migrated the single admin password to user \"admin\"")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(st.get().Users) == 0 && cfg.AdminPassword != "" {
		if err := validUsername(cfg.AdminUser); err != nil {
			return nil, fmt.Errorf("STRAWLOG_ADMIN_USER: %w", err)
		}
		h, err := HashPassword(cfg.AdminPassword)
		if err != nil {
			return nil, fmt.Errorf("STRAWLOG_ADMIN_PASSWORD: %w", err)
		}
		if err := st.update(func(hs *hubState) error {
			hs.Users = append(hs.Users, User{ID: newID(), Username: cfg.AdminUser, PasswordHash: h, Role: RoleAdmin, CreatedAt: time.Now().Unix()})
			return nil
		}); err != nil {
			return nil, err
		}
		s.log.Info("created admin user from STRAWLOG_ADMIN_PASSWORD", "user", cfg.AdminUser)
	} else if cfg.AdminPassword != "" {
		s.log.Info("users already exist; STRAWLOG_ADMIN_PASSWORD is ignored (use `strawlog hub set-password` to reset)")
	}
	if len(st.get().Users) == 0 {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		s.setupToken = hex.EncodeToString(b)
		fmt.Fprintf(os.Stderr, "\n================ STRAW LOG FIRST-RUN SETUP ================\nOpen the dashboard and enter this setup token to create the admin password:\n%s\n===========================================================\n\n", s.setupToken)
	}
	for _, n := range st.get().Nodes {
		if err := s.connect(n); err != nil {
			s.log.Warn("node", "name", n.Name, "err", err)
		}
	}
	return s, nil
}

func (s *Server) connect(n NodeRec) error {
	pin, err := hex.DecodeString(n.Fingerprint)
	if err != nil || len(pin) != 32 {
		return errors.New("bad stored fingerprint")
	}
	s.mu.Lock()
	s.remotes[n.ID] = NewRemote(n.Addr, s.client, pin)
	s.mu.Unlock()
	return nil
}

func (s *Server) node(id string) (api.Node, error) {
	if id == LocalID {
		if s.cfg.Local == nil {
			return nil, errors.New("local node is disabled")
		}
		return s.cfg.Local, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.remotes[id]
	if !ok {
		return nil, fmt.Errorf("unknown node %q", id)
	}
	return r, nil
}

func (s *Server) setupRequired() bool { return len(s.store.get().Users) == 0 }

// ---------- HTTP plumbing ----------

func (s *Server) cookieName() string {
	if s.secure {
		return "__Host-strawlog"
	}
	return "strawlog"
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// currentUser resolves the session cookie to an existing, enabled user. Because the user is
// looked up on every request, disabling or deleting a user takes effect immediately.
func (s *Server) currentUser(r *http.Request) (User, bool) {
	c, err := r.Cookie(s.cookieName())
	if err != nil {
		return User{}, false
	}
	id, ok := s.sess.lookup(c.Value)
	if !ok {
		return User{}, false
	}
	st := s.store.get()
	u, ok := st.userByID(id)
	if !ok || u.Disabled {
		s.sess.revoke(c.Value)
		return User{}, false
	}
	return u, true
}

// guard enforces session auth, the minimum role and CSRF protection for state-changing requests.
func (s *Server) guard(min Role, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(r)
		if !ok {
			jsonErr(w, http.StatusUnauthorized, errors.New("login required"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !s.sameOrigin(r) {
				jsonErr(w, http.StatusForbidden, errors.New("cross-origin request rejected"))
				return
			}
		}
		if u.Role.rank() < min.rank() {
			jsonErr(w, http.StatusForbidden, fmt.Errorf("requires the %s role", min))
			return
		}
		if r.Method != http.MethodGet && min.rank() >= RoleOperator.rank() {
			s.log.Info("audit", "user", u.Username, "method", r.Method, "path", r.URL.Path, "ip", clientIP(r))
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

func (s *Server) sameOrigin(r *http.Request) bool {
	if r.Header.Get("X-Strawlog") != "1" { // custom header => forces CORS preflight, which we never allow
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		o = strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://")
		return o == r.Host
	}
	return true
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, err error) {
	jsonOut(w, code, map[string]string{"error": err.Error()})
}

func readBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// --- auth ---
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(r)
		out := map[string]any{"authenticated": ok, "setupRequired": s.setupRequired(), "version": api.Version}
		if ok {
			out["user"] = u.public()
		}
		jsonOut(w, 200, out)
	})
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(s.cookieName()); err == nil {
			s.sess.revoke(c.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode})
		jsonOut(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/password", s.guard(RoleViewer, s.handlePassword))

	// --- users (admin) ---
	mux.HandleFunc("GET /api/users", s.guard(RoleAdmin, s.handleListUsers))
	mux.HandleFunc("POST /api/users", s.guard(RoleAdmin, s.handleCreateUser))
	mux.HandleFunc("PATCH /api/users/{id}", s.guard(RoleAdmin, s.handleUpdateUser))
	mux.HandleFunc("DELETE /api/users/{id}", s.guard(RoleAdmin, s.handleDeleteUser))

	// --- notifications (admin) ---
	mux.HandleFunc("GET /api/notifications", s.guard(RoleAdmin, s.handleListChannels))
	mux.HandleFunc("POST /api/notifications", s.guard(RoleAdmin, s.handleSaveChannel))
	mux.HandleFunc("PUT /api/notifications/{id}", s.guard(RoleAdmin, s.handleSaveChannel))
	mux.HandleFunc("DELETE /api/notifications/{id}", s.guard(RoleAdmin, s.handleDeleteChannel))
	mux.HandleFunc("POST /api/notifications/test", s.guard(RoleAdmin, s.handleTestChannel))

	// --- nodes ---
	mux.HandleFunc("GET /api/nodes", s.guard(RoleViewer, s.handleNodes))
	mux.HandleFunc("POST /api/nodes", s.guard(RoleAdmin, s.handleAddNode))
	mux.HandleFunc("PATCH /api/nodes/{id}", s.guard(RoleAdmin, s.handleRenameNode))
	mux.HandleFunc("DELETE /api/nodes/{id}", s.guard(RoleAdmin, s.handleDeleteNode))
	mux.HandleFunc("GET /api/overview", s.guard(RoleViewer, s.handleOverview))
	mux.HandleFunc("POST /api/search", s.guard(RoleViewer, s.handleSearchAll))

	// --- per node proxy ---
	n := func(fn func(w http.ResponseWriter, r *http.Request, n api.Node), roles ...Role) http.HandlerFunc {
		min := RoleViewer
		if len(roles) > 0 {
			min = roles[0]
		}
		return s.guard(min, func(w http.ResponseWriter, r *http.Request) {
			nd, err := s.node(r.PathValue("id"))
			if err != nil {
				jsonErr(w, 404, err)
				return
			}
			fn(w, r, nd)
		})
	}
	reply := func(w http.ResponseWriter, v any, err error) {
		if err != nil {
			jsonErr(w, http.StatusBadGateway, err)
			return
		}
		jsonOut(w, 200, v)
	}
	mux.HandleFunc("GET /api/n/{id}/info", n(func(w http.ResponseWriter, r *http.Request, nd api.Node) {
		v, err := nd.Info(r.Context())
		reply(w, v, err)
	}))
	mux.HandleFunc("GET /api/n/{id}/sources", n(func(w http.ResponseWriter, r *http.Request, nd api.Node) {
		v, err := nd.Sources(r.Context(), r.URL.Query().Get("refresh") == "1")
		if err != nil && len(v) > 0 {
			err = nil
		}
		if v == nil {
			v = []api.Source{}
		}
		reply(w, v, err)
	}))
	mux.HandleFunc("GET /api/n/{id}/health", n(func(w http.ResponseWriter, r *http.Request, nd api.Node) {
		v, err := nd.Health(r.Context())
		if v == nil {
			v = []api.Finding{}
		}
		reply(w, v, err)
	}))
	mux.HandleFunc("GET /api/n/{id}/history", n(func(w http.ResponseWriter, r *http.Request, nd api.Node) {
		v, err := nd.History(r.Context())
		reply(w, v, err)
	}))
	mux.HandleFunc("GET /api/n/{id}/policies", n(func(w http.ResponseWriter, r *http.Request, nd api.Node) {
		v, err := nd.Policies(r.Context())
		if v == nil {
			v = []api.Policy{}
		}
		reply(w, v, err)
	}))
	mux.HandleFunc("PUT /api/n/{id}/policies", n(func(w http.ResponseWriter, r *http.Request, nd api.Node) {
		var p []api.Policy
		if err := readBody(r, &p); err != nil {
			jsonErr(w, 400, err)
			return
		}
		if err := nd.SetPolicies(r.Context(), p); err != nil {
			jsonErr(w, 400, err)
			return
		}
		v, err := nd.Policies(r.Context())
		reply(w, v, err)
	}, RoleOperator))
	mux.HandleFunc("POST /api/n/{id}/plan", n(func(w http.ResponseWriter, r *http.Request, nd api.Node) {
		var p api.PlanReq
		if err := readBody(r, &p); err != nil {
			jsonErr(w, 400, err)
			return
		}
		v, err := nd.Plan(r.Context(), p)
		reply(w, v, err)
	}))
	mux.HandleFunc("POST /api/n/{id}/apply", n(func(w http.ResponseWriter, r *http.Request, nd api.Node) {
		var p api.PlanReq
		if err := readBody(r, &p); err != nil {
			jsonErr(w, 400, err)
			return
		}
		v, err := nd.Apply(r.Context(), p)
		reply(w, v, err)
	}, RoleOperator))
	mux.HandleFunc("GET /api/n/{id}/runs", n(func(w http.ResponseWriter, r *http.Request, nd api.Node) {
		v, err := nd.Runs(r.Context())
		if v == nil {
			v = []api.Plan{}
		}
		reply(w, v, err)
	}))
	mux.HandleFunc("POST /api/n/{id}/tail", n(func(w http.ResponseWriter, r *http.Request, nd api.Node) {
		var q api.TailReq
		if err := readBody(r, &q); err != nil {
			jsonErr(w, 400, err)
			return
		}
		v, err := nd.Tail(r.Context(), q)
		if v == nil {
			v = []api.Line{}
		}
		reply(w, v, err)
	}))
	mux.HandleFunc("POST /api/n/{id}/search", n(func(w http.ResponseWriter, r *http.Request, nd api.Node) {
		var q api.SearchReq
		if err := readBody(r, &q); err != nil {
			jsonErr(w, 400, err)
			return
		}
		v, err := nd.Search(r.Context(), q)
		if v.Lines == nil {
			v.Lines = []api.Line{}
		}
		reply(w, v, err)
	}))
	mux.HandleFunc("GET /api/n/{id}/follow", n(s.handleFollow))
	mux.HandleFunc("GET /api/n/{id}/download", n(s.handleDownload))

	mux.Handle("/", s.static())
	return securityHeaders(mux)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.loginLim.allow("setup:" + clientIP(r)) {
		jsonErr(w, 429, errors.New("too many attempts"))
		return
	}
	if !s.setupRequired() || s.setupToken == "" {
		jsonErr(w, 409, errors.New("already set up"))
		return
	}
	var req struct{ Token, Username, Password string }
	if err := readBody(r, &req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.Token)), []byte(s.setupToken)) != 1 {
		time.Sleep(500 * time.Millisecond)
		jsonErr(w, 403, errors.New("wrong setup token (see the hub container logs)"))
		return
	}
	req.Username = strings.TrimSpace(firstNonEmpty(req.Username, "admin"))
	if err := validUsername(req.Username); err != nil {
		jsonErr(w, 400, err)
		return
	}
	h, err := HashPassword(req.Password)
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	u := User{ID: newID(), Username: req.Username, PasswordHash: h, Role: RoleAdmin, CreatedAt: time.Now().Unix(), LastLogin: time.Now().Unix()}
	err = s.store.update(func(st *hubState) error {
		if len(st.Users) > 0 {
			return errors.New("already set up")
		}
		st.Users = append(st.Users, u)
		return nil
	})
	if err != nil {
		jsonErr(w, 409, err)
		return
	}
	s.setupToken = ""
	s.startSession(w, u.ID)
	jsonOut(w, 200, map[string]any{"ok": true, "user": u.public()})
}

func (s *Server) startSession(w http.ResponseWriter, userID string) {
	tok := s.sess.create(userID)
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: tok, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionAbsolute.Seconds())})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	if err := readBody(r, &req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		req.Username = "admin"
	}
	ip := clientIP(r)
	if !s.loginLim.allow(ip) || !s.userLim.allow(strings.ToLower(req.Username)) {
		jsonErr(w, 429, errors.New("too many login attempts, try again in a few minutes"))
		return
	}
	st := s.store.get()
	u, i := st.userByName(req.Username)
	hash := dummyHash
	if i >= 0 {
		hash = u.PasswordHash
	}
	if !checkPassword(hash, req.Password) || i < 0 || u.Disabled {
		time.Sleep(400 * time.Millisecond)
		s.log.Warn("failed login", "ip", ip, "user", trunc(req.Username, 32))
		if ev, ok := s.failed.record(ip, trunc(req.Username, 32)); ok {
			s.emit(ev)
		}
		jsonErr(w, 401, errors.New("wrong username or password"))
		return
	}
	_ = s.store.update(func(hs *hubState) error {
		if _, j := hs.userByName(u.Username); j >= 0 {
			hs.Users[j].LastLogin = time.Now().Unix()
		}
		return nil
	})
	s.log.Info("login", "user", u.Username, "ip", ip)
	s.startSession(w, u.ID)
	jsonOut(w, 200, map[string]any{"ok": true, "user": u.public()})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct{ Current, New string }
	if err := readBody(r, &req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	me := userFrom(r)
	if !checkPassword(me.PasswordHash, req.Current) {
		jsonErr(w, 403, errors.New("current password is wrong"))
		return
	}
	h, err := HashPassword(req.New)
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	if err := s.store.update(func(st *hubState) error {
		_, i := st.userByName(me.Username)
		if i < 0 {
			return errors.New("user vanished")
		}
		st.Users[i].PasswordHash = h
		return nil
	}); err != nil {
		jsonErr(w, 500, err)
		return
	}
	keep := ""
	if c, err := r.Cookie(s.cookieName()); err == nil {
		keep = c.Value
	}
	s.sess.revokeUser(me.ID, keep) // sign out other devices
	jsonOut(w, 200, map[string]bool{"ok": true})
}

// ---------- nodes ----------

type nodeView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Addr      string    `json:"addr,omitempty"`
	Local     bool      `json:"local"`
	Online    bool      `json:"online"`
	Error     string    `json:"error,omitempty"`
	LatencyMs int64     `json:"latencyMs"`
	Info      *api.Info `json:"info,omitempty"`
	Fp        string    `json:"fingerprint,omitempty"`
}

func (s *Server) nodeList() []nodeView {
	var out []nodeView
	if s.cfg.Local != nil {
		out = append(out, nodeView{ID: LocalID, Name: s.cfg.LocalName, Local: true})
	}
	for _, n := range s.store.get().Nodes {
		out = append(out, nodeView{ID: n.ID, Name: n.Name, Addr: n.Addr, Fp: n.Fingerprint[:16]})
	}
	return out
}

func (s *Server) probe(ctx context.Context, views []nodeView) {
	var wg sync.WaitGroup
	for i := range views {
		wg.Add(1)
		go func(v *nodeView) {
			defer wg.Done()
			nd, err := s.node(v.ID)
			if err != nil {
				v.Error = err.Error()
				return
			}
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			t := time.Now()
			info, err := nd.Info(c)
			v.LatencyMs = time.Since(t).Milliseconds()
			if err != nil {
				v.Error = err.Error()
				return
			}
			v.Online, v.Info = true, &info
			if v.Local && v.Name == "" {
				v.Name = info.Hostname
			}
		}(&views[i])
	}
	wg.Wait()
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	views := s.nodeList()
	s.probe(r.Context(), views)
	jsonOut(w, 200, views)
}

func normalizeAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(strings.TrimPrefix(addr, "https://"), "http://")
	addr = strings.TrimRight(addr, "/")
	if addr == "" {
		return "", errors.New("address is required")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(strings.Trim(addr, "[]"), "7443")
	}
	h, p, err := net.SplitHostPort(addr)
	if err != nil || h == "" {
		return "", fmt.Errorf("invalid address %q", addr)
	}
	if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("invalid port %q", p)
	}
	return addr, nil
}

func (s *Server) handleAddNode(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Addr, Code string }
	if err := readBody(r, &req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	addr, err := normalizeAddr(req.Addr)
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	fp, tok, err := agent.ParsePairingCode(req.Code)
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	fpHex := hex.EncodeToString(fp)
	for _, n := range s.store.get().Nodes {
		if n.Fingerprint == fpHex {
			jsonErr(w, 409, fmt.Errorf("this agent is already added as %q", n.Name))
			return
		}
	}
	rem := NewRemote(addr, s.client, fp)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	hubName, _ := os.Hostname()
	info, err := rem.Pair(ctx, tok, "hub@"+hubName)
	if err != nil {
		jsonErr(w, 502, fmt.Errorf("pairing failed: %v", err))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = info.Hostname
	}
	idb := make([]byte, 6)
	_, _ = rand.Read(idb)
	rec := NodeRec{ID: base64.RawURLEncoding.EncodeToString(idb), Name: trunc(name, 64), Addr: addr, Fingerprint: fpHex, AddedAt: time.Now().Unix()}
	if err := s.store.update(func(st *hubState) error { st.Nodes = append(st.Nodes, rec); return nil }); err != nil {
		jsonErr(w, 500, err)
		return
	}
	s.mu.Lock()
	s.remotes[rec.ID] = rem
	s.mu.Unlock()
	s.log.Info("node paired", "name", rec.Name, "addr", addr)
	jsonOut(w, 200, rec)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Server) handleRenameNode(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, Addr string }
	if err := readBody(r, &req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	id := r.PathValue("id")
	var updated NodeRec
	err := s.store.update(func(st *hubState) error {
		for i := range st.Nodes {
			if st.Nodes[i].ID == id {
				if n := strings.TrimSpace(req.Name); n != "" {
					st.Nodes[i].Name = trunc(n, 64)
				}
				if req.Addr != "" {
					a, err := normalizeAddr(req.Addr)
					if err != nil {
						return err
					}
					st.Nodes[i].Addr = a
				}
				updated = st.Nodes[i]
				return nil
			}
		}
		return errors.New("unknown node")
	})
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	_ = s.connect(updated)
	jsonOut(w, 200, updated)
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.store.update(func(st *hubState) error {
		for i := range st.Nodes {
			if st.Nodes[i].ID == id {
				st.Nodes = append(st.Nodes[:i], st.Nodes[i+1:]...)
				return nil
			}
		}
		return errors.New("unknown node")
	})
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	s.mu.Lock()
	delete(s.remotes, id)
	s.mu.Unlock()
	jsonOut(w, 200, map[string]bool{"ok": true})
}

// ---------- overview / fan-out ----------

type overviewNode struct {
	nodeView
	Top      []api.Source  `json:"top"`
	Apps     []appTotal    `json:"apps"`
	Findings []api.Finding `json:"findings"`
	Growth   int64         `json:"growth24h"`
}

type appTotal struct {
	App     string `json:"app"`
	Bytes   int64  `json:"bytes"`
	Growth  int64  `json:"growth24h"`
	Sources int    `json:"sources"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	views := s.nodeList()
	s.probe(r.Context(), views)
	out := make([]overviewNode, len(views))
	var wg sync.WaitGroup
	for i := range views {
		out[i].nodeView = views[i]
		if !views[i].Online {
			continue
		}
		wg.Add(1)
		go func(o *overviewNode) {
			defer wg.Done()
			nd, _ := s.node(o.ID)
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			srcs, _ := nd.Sources(ctx, false)
			apps := map[string]*appTotal{}
			for _, src := range srcs {
				a := apps[src.App]
				if a == nil {
					a = &appTotal{App: src.App}
					apps[src.App] = a
				}
				a.Bytes += src.TotalSize
				a.Growth += src.Growth24h
				a.Sources++
				o.Growth += src.Growth24h
			}
			for _, a := range apps {
				o.Apps = append(o.Apps, *a)
			}
			sort.Slice(o.Apps, func(i, j int) bool { return o.Apps[i].Bytes > o.Apps[j].Bytes })
			sort.Slice(srcs, func(i, j int) bool { return srcs[i].TotalSize > srcs[j].TotalSize })
			o.Top = srcs[:min(10, len(srcs))]
			o.Findings, _ = nd.Health(ctx)
		}(&out[i])
	}
	wg.Wait()
	jsonOut(w, 200, out)
}

func (s *Server) handleSearchAll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nodes []string `json:"nodes"`
		api.SearchReq
	}
	if err := readBody(r, &req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	if len(req.Nodes) == 0 {
		for _, v := range s.nodeList() {
			req.Nodes = append(req.Nodes, v.ID)
		}
	}
	type res struct {
		api.SearchResult
		Error string `json:"error,omitempty"`
	}
	out := map[string]*res{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, id := range req.Nodes {
		nd, err := s.node(id)
		if err != nil {
			out[id] = &res{Error: err.Error()}
			continue
		}
		wg.Add(1)
		go func(id string, nd api.Node) {
			defer wg.Done()
			v, err := nd.Search(r.Context(), req.SearchReq)
			rr := &res{SearchResult: v}
			if err != nil {
				rr.Error = err.Error()
			}
			if rr.Lines == nil {
				rr.Lines = []api.Line{}
			}
			mu.Lock()
			out[id] = rr
			mu.Unlock()
		}(id, nd)
	}
	wg.Wait()
	jsonOut(w, 200, out)
}

// handleFollow streams lines to the browser as Server-Sent Events.
func (s *Server) handleFollow(w http.ResponseWriter, r *http.Request, nd api.Node) {
	if s.follows.Add(1) > 32 {
		s.follows.Add(-1)
		jsonErr(w, 429, errors.New("too many live streams open"))
		return
	}
	defer s.follows.Add(-1)
	q := r.URL.Query()
	req := api.TailReq{Source: q.Get("source"), Unit: q.Get("unit")}
	req.Lines, _ = strconv.Atoi(q.Get("lines"))
	fl, ok := w.(http.Flusher)
	if !ok {
		jsonErr(w, 500, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fl.Flush()
	var mu sync.Mutex
	dirty := false
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		hb := time.NewTicker(20 * time.Second)
		defer t.Stop()
		defer hb.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-hb.C:
				mu.Lock()
				_, _ = io.WriteString(w, ": ping\n\n")
				dirty = true
				mu.Unlock()
			case <-t.C:
			}
			mu.Lock()
			if dirty {
				fl.Flush()
				dirty = false
			}
			mu.Unlock()
		}
	}()
	err := nd.Follow(ctx, req, func(l api.Line) error {
		b, _ := json.Marshal(l)
		mu.Lock()
		defer mu.Unlock()
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return err
		}
		dirty = true
		return nil
	})
	cancel()
	mu.Lock()
	defer mu.Unlock()
	if err != nil && r.Context().Err() == nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", b)
	}
	fmt.Fprint(w, "event: end\ndata: {}\n\n")
	fl.Flush()
}

// handleDownload returns the current tail as a plain-text file.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request, nd api.Node) {
	q := r.URL.Query()
	req := api.TailReq{Source: q.Get("source"), Unit: q.Get("unit"), Lines: 5000}
	lines, err := nd.Tail(r.Context(), req)
	if err != nil {
		jsonErr(w, 502, err)
		return
	}
	name := strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(req.Source)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.log"`, name))
	for _, l := range lines {
		if l.Time != "" {
			io.WriteString(w, l.Time+" ")
		}
		io.WriteString(w, l.Text+"\n")
	}
}

// ---------- static UI ----------

func (s *Server) static() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			jsonErr(w, 404, errors.New("not found"))
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			r.URL.Path = "/"
			p = "index.html"
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

// ---------- run ----------

func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	go s.monitor(ctx, s.cfg.NotifyInterval)
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	var err error
	switch mode := s.cfg.TLS; {
	case mode == "" || mode == "off":
		s.log.Info("hub listening (plain HTTP — keep it on localhost/LAN or behind a TLS reverse proxy)", "addr", s.cfg.Listen)
		err = srv.ListenAndServe()
	case mode == "auto":
		host, _ := os.Hostname()
		cert, cerr := tlsutil.LoadOrCreate(filepath.Join(s.cfg.DataDir, "hub-web.crt"), filepath.Join(s.cfg.DataDir, "hub-web.key"), host)
		if cerr != nil {
			return cerr
		}
		s.secure = true
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
		s.log.Info("hub listening (HTTPS, self-signed)", "addr", s.cfg.Listen, "sha256", tlsutil.FingerprintHex(cert.Certificate[0]))
		err = srv.ListenAndServeTLS("", "")
	default:
		certFile, keyFile, ok := strings.Cut(mode, ":")
		if !ok {
			return errors.New("STRAWLOG_TLS must be off, auto or cert.pem:key.pem")
		}
		s.secure = true
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		s.log.Info("hub listening (HTTPS)", "addr", s.cfg.Listen)
		err = srv.ListenAndServeTLS(certFile, keyFile)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// SecureCookies forces Secure cookies (use when a TLS reverse proxy terminates HTTPS).
func (s *Server) SecureCookies() { s.secure = true }
