// Package agent serves a node's API over mutually-authenticated TLS.
//
// Trust model:
//   - The agent has a self-signed identity; its SHA-256 fingerprint is part of the pairing code,
//     so the hub pins it from the very first connection (no TOFU window).
//   - A one-time pairing token (128 bit, 24h expiry, stored hashed) lets one hub register its
//     client-certificate fingerprint. Afterwards only pinned hub certificates get any API access.
//   - Connections from outside STRAWLOG_ALLOW_FROM are dropped before the TLS handshake.
package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"strawlog/internal/api"
	"strawlog/internal/tlsutil"
)

const pairingTTL = 24 * time.Hour

type Config struct {
	Listen    string
	DataDir   string
	AllowFrom []netip.Prefix
	Node      api.Node
	Log       *slog.Logger
}

type PairedHub struct {
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name,omitempty"`
	Addr        string `json:"addr,omitempty"`
	PairedAt    int64  `json:"pairedAt"`
}

type state struct {
	Hubs    []PairedHub `json:"hubs"`
	Pairing *struct {
		TokenHash string `json:"tokenHash"`
		Expires   int64  `json:"expires"`
	} `json:"pairing,omitempty"`
}

type Server struct {
	cfg   Config
	cert  tls.Certificate
	log   *slog.Logger
	mu    sync.Mutex
	st    state
	stMod time.Time
	api   http.Handler

	pairMu    sync.Mutex
	pairTimes []time.Time
}

func statePath(dir string) string { return filepath.Join(dir, "agent-state.json") }

func Identity(dataDir string) (tls.Certificate, error) {
	host, _ := os.Hostname()
	return tlsutil.LoadOrCreate(filepath.Join(dataDir, "agent.crt"), filepath.Join(dataDir, "agent.key"), "strawlog-agent-"+host)
}

func loadState(dir string) (state, time.Time, error) {
	var st state
	fi, err := os.Stat(statePath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return st, time.Time{}, nil
	}
	if err != nil {
		return st, time.Time{}, err
	}
	b, err := os.ReadFile(statePath(dir))
	if err != nil {
		return st, time.Time{}, err
	}
	return st, fi.ModTime(), json.Unmarshal(b, &st)
}

func saveState(dir string, st state) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := statePath(dir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(dir))
}

// NewPairingCode creates (and persists) a fresh one-time pairing code.
// Code format: slp1.<base64url(agent cert sha256)>.<base64url(token)>
func NewPairingCode(dataDir string) (string, error) {
	cert, err := Identity(dataDir)
	if err != nil {
		return "", err
	}
	st, _, err := loadState(dataDir)
	if err != nil {
		return "", err
	}
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return "", err
	}
	h := sha256.Sum256(tok)
	st.Pairing = &struct {
		TokenHash string `json:"tokenHash"`
		Expires   int64  `json:"expires"`
	}{hex.EncodeToString(h[:]), time.Now().Add(pairingTTL).Unix()}
	if err := saveState(dataDir, st); err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	return "slp1." + enc.EncodeToString(tlsutil.Fingerprint(cert.Certificate[0])) + "." + enc.EncodeToString(tok), nil
}

// ParsePairingCode returns agent fingerprint and token.
func ParsePairingCode(code string) (fp, token []byte, err error) {
	parts := strings.Split(strings.TrimSpace(code), ".")
	if len(parts) != 3 || parts[0] != "slp1" {
		return nil, nil, errors.New("invalid pairing code (expected slp1.<fingerprint>.<token>)")
	}
	enc := base64.RawURLEncoding
	if fp, err = enc.DecodeString(parts[1]); err != nil || len(fp) != 32 {
		return nil, nil, errors.New("invalid pairing code fingerprint")
	}
	if token, err = enc.DecodeString(parts[2]); err != nil || len(token) != 16 {
		return nil, nil, errors.New("invalid pairing code token")
	}
	return fp, token, nil
}

// Unpair removes all paired hubs (or one by fingerprint prefix).
func Unpair(dataDir, fpPrefix string) (int, error) {
	st, _, err := loadState(dataDir)
	if err != nil {
		return 0, err
	}
	var keep []PairedHub
	removed := 0
	for _, h := range st.Hubs {
		if fpPrefix == "" || strings.HasPrefix(h.Fingerprint, fpPrefix) {
			removed++
			continue
		}
		keep = append(keep, h)
	}
	st.Hubs = keep
	return removed, saveState(dataDir, st)
}

func ListHubs(dataDir string) ([]PairedHub, error) {
	st, _, err := loadState(dataDir)
	return st.Hubs, err
}

func New(cfg Config) (*Server, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	cert, err := Identity(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, cert: cert, log: cfg.Log, api: NodeHandler(cfg.Node)}
	if err := s.reload(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	needCode := len(s.st.Hubs) == 0 && (s.st.Pairing == nil || time.Now().Unix() > s.st.Pairing.Expires)
	s.mu.Unlock()
	if needCode {
		code, err := NewPairingCode(cfg.DataDir)
		if err != nil {
			return nil, err
		}
		s.announce(code)
		_ = s.reload()
	} else if len(s.st.Hubs) == 0 {
		s.log.Info("waiting for pairing; run `strawlog agent pair` to print a new code")
	}
	s.log.Info("agent identity", "fingerprint", tlsutil.FingerprintHex(cert.Certificate[0]))
	return s, nil
}

func (s *Server) announce(code string) {
	fmt.Fprintf(os.Stderr, "\n================ STRAW LOG PAIRING CODE (valid 24h, single use) ================\n%s\n"+
		"In the dashboard: Nodes → Add agent → paste this code and this server's address (host:%s).\n"+
		"=================================================================================\n\n", code, portOf(s.cfg.Listen))
}

func portOf(listen string) string {
	_, p, err := net.SplitHostPort(listen)
	if err != nil {
		return "7443"
	}
	return p
}

// reload re-reads state if the file changed (so the CLI can pair/unpair while running).
func (s *Server) reload() error {
	fi, err := os.Stat(statePath(s.cfg.DataDir))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil && fi.ModTime().Equal(s.stMod) {
		return nil
	}
	st, mod, err := loadState(s.cfg.DataDir)
	if err != nil {
		return err
	}
	s.st, s.stMod = st, mod
	return nil
}

func (s *Server) paired(fp string) bool {
	_ = s.reload()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.st.Hubs {
		if subtle.ConstantTimeCompare([]byte(h.Fingerprint), []byte(fp)) == 1 {
			return true
		}
	}
	return false
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "client certificate required", http.StatusUnauthorized)
		return
	}
	fp := tlsutil.FingerprintHex(r.TLS.PeerCertificates[0].Raw)
	if r.URL.Path == "/v1/pair" && r.Method == http.MethodPost {
		s.handlePair(w, r, fp)
		return
	}
	if !s.paired(fp) {
		s.log.Warn("rejected unpaired client", "remote", r.RemoteAddr, "fingerprint", fp[:16])
		writeErr(w, r, http.StatusForbidden, errors.New("this hub is not paired with the agent"))
		return
	}
	s.api.ServeHTTP(w, r)
}

func (s *Server) allowPairAttempt() bool {
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	now := time.Now()
	keep := s.pairTimes[:0]
	for _, t := range s.pairTimes {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	s.pairTimes = keep
	if len(keep) >= 5 {
		return false
	}
	s.pairTimes = append(s.pairTimes, now)
	return true
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request, fp string) {
	if !s.allowPairAttempt() {
		writeErr(w, r, http.StatusTooManyRequests, errors.New("too many pairing attempts, wait a minute"))
		return
	}
	var req struct {
		Token string `json:"token"`
		Name  string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, r, 400, err)
		return
	}
	tok, err := base64.RawURLEncoding.DecodeString(req.Token)
	if err != nil {
		writeErr(w, r, 403, errors.New("invalid pairing token"))
		return
	}
	_ = s.reload()
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.st.Pairing
	h := sha256.Sum256(tok)
	if p == nil || time.Now().Unix() > p.Expires || subtle.ConstantTimeCompare([]byte(hex.EncodeToString(h[:])), []byte(p.TokenHash)) != 1 {
		s.log.Warn("failed pairing attempt", "remote", r.RemoteAddr)
		writeErr(w, r, 403, errors.New("pairing token invalid, expired or already used; run `strawlog agent pair` on the server for a new one"))
		return
	}
	st := s.st
	st.Pairing = nil
	already := false
	for _, hb := range st.Hubs {
		if hb.Fingerprint == fp {
			already = true
		}
	}
	if !already {
		st.Hubs = append(st.Hubs, PairedHub{Fingerprint: fp, Name: trunc(req.Name, 64), Addr: r.RemoteAddr, PairedAt: time.Now().Unix()})
	}
	if err := saveState(s.cfg.DataDir, st); err != nil {
		writeErr(w, r, 500, err)
		return
	}
	s.st = st
	if fi, err := os.Stat(statePath(s.cfg.DataDir)); err == nil {
		s.stMod = fi.ModTime()
	}
	s.log.Info("hub paired", "remote", r.RemoteAddr, "fingerprint", fp[:16], "name", req.Name)
	info, _ := s.cfg.Node.Info(r.Context())
	writeJSON(w, r, 200, info)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// allowListener drops connections from outside the allowed prefixes before any TLS work.
type allowListener struct {
	net.Listener
	allow []netip.Prefix
	log   *slog.Logger
}

func (l *allowListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ap, err := netip.ParseAddrPort(c.RemoteAddr().String())
		if err == nil {
			addr := ap.Addr().Unmap()
			if addr.IsLoopback() { // the container's own healthcheck
				return c, nil
			}
			for _, p := range l.allow {
				if p.Contains(addr) {
					return c, nil
				}
			}
		}
		l.log.Warn("dropped connection (not in STRAWLOG_ALLOW_FROM)", "remote", c.RemoteAddr().String())
		c.Close()
	}
}

func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	if len(s.cfg.AllowFrom) > 0 {
		ln = &allowListener{Listener: ln, allow: s.cfg.AllowFrom, log: s.log}
	}
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{s.cert},
			ClientAuth:   tls.RequireAnyClientCert, // identity is checked by fingerprint in ServeHTTP
		},
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	s.log.Info("agent listening", "addr", s.cfg.Listen, "allowFrom", s.cfg.AllowFrom)
	err = srv.ServeTLS(ln, "", "")
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
