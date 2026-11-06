package hub

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pbkdfIter       = 600_000
	sessionIdle     = 12 * time.Hour
	sessionAbsolute = 7 * 24 * time.Hour
	minPasswordLen  = 10
)

// ---------- roles & users ----------

type Role string

const (
	RoleViewer   Role = "viewer"   // read everything, search, tail, preview retention
	RoleOperator Role = "operator" // + edit and apply retention policies
	RoleAdmin    Role = "admin"    // + nodes, users, notifications
)

func (r Role) rank() int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleOperator:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

func (r Role) valid() bool { return r.rank() > 0 }

type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash,omitempty"`
	Role         Role   `json:"role"`
	Disabled     bool   `json:"disabled,omitempty"`
	CreatedAt    int64  `json:"createdAt"`
	LastLogin    int64  `json:"lastLogin,omitempty"`
}

// public returns the user without secrets.
func (u User) public() User { u.PasswordHash = ""; return u }

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{2,32}$`)

func validUsername(u string) error {
	if !usernameRe.MatchString(u) {
		return errors.New("username must be 2-32 characters: letters, digits, . _ -")
	}
	return nil
}

// ---------- persistent hub state ----------

type NodeRec struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Addr        string `json:"addr"`
	Fingerprint string `json:"fingerprint"` // hex sha256 of agent cert
	AddedAt     int64  `json:"addedAt"`
}

type hubState struct {
	PasswordHash string    `json:"passwordHash,omitempty"` // legacy single-admin (migrated to Users)
	Users        []User    `json:"users"`
	Nodes        []NodeRec `json:"nodes"`
	Channels     []Channel `json:"channels"`
}

func (st *hubState) clone() hubState {
	c := *st
	c.Users = slices.Clone(st.Users)
	c.Nodes = slices.Clone(st.Nodes)
	c.Channels = make([]Channel, len(st.Channels))
	for i, ch := range st.Channels {
		ch.Events = slices.Clone(ch.Events)
		ch.Nodes = slices.Clone(ch.Nodes)
		c.Channels[i] = ch
	}
	return c
}

func (st *hubState) userByID(id string) (User, bool) {
	for _, u := range st.Users {
		if u.ID == id {
			return u, true
		}
	}
	return User{}, false
}

func (st *hubState) userByName(name string) (User, int) {
	for i, u := range st.Users {
		if strings.EqualFold(u.Username, name) {
			return u, i
		}
	}
	return User{}, -1
}

func (st *hubState) enabledAdmins() int {
	n := 0
	for _, u := range st.Users {
		if u.Role == RoleAdmin && !u.Disabled {
			n++
		}
	}
	return n
}

// store persists hubState to hub.json (0600). It reloads when the file is changed
// externally (e.g. by `strawlog hub set-password`) so the CLI never gets overwritten.
type store struct {
	path string
	mu   sync.Mutex
	st   hubState
	mod  time.Time
}

func openStore(dir string) (*store, error) {
	s := &store{path: filepath.Join(dir, "hub.json")}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s, s.reloadLocked(true)
}

func (s *store) reloadLocked(force bool) error {
	fi, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !force && fi.ModTime().Equal(s.mod) {
		return nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var st hubState
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("%s: %w", s.path, err)
	}
	s.st, s.mod = st, fi.ModTime()
	return nil
}

func (s *store) update(fn func(*hubState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reloadLocked(false)
	next := s.st.clone()
	if err := fn(&next); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(next, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.st = next
	if fi, err := os.Stat(s.path); err == nil {
		s.mod = fi.ModTime()
	}
	return nil
}

func (s *store) get() hubState {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.reloadLocked(false)
	return s.st.clone()
}

// ---------- passwords ----------

func HashPassword(pw string) (string, error) {
	if len(pw) < minPasswordLen {
		return "", fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	if len(pw) > 256 {
		return "", errors.New("password too long")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	k, err := pbkdf2.Key(sha256.New, pw, salt, pbkdfIter, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdfIter, enc.EncodeToString(salt), enc.EncodeToString(k)), nil
}

// dummyHash is checked when a username doesn't exist, so timing doesn't reveal valid usernames.
var dummyHash = func() string { h, _ := HashPassword("dummy-password-for-timing"); return h }()

func checkPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 100_000 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	k, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(k, want) == 1
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// SetPassword is used by `strawlog hub set-password [username]`. Creates an admin if the user doesn't exist.
func SetPassword(dataDir, username, pw string) (created bool, err error) {
	if err := validUsername(username); err != nil {
		return false, err
	}
	h, err := HashPassword(pw)
	if err != nil {
		return false, err
	}
	s, err := openStore(dataDir)
	if err != nil {
		return false, err
	}
	err = s.update(func(st *hubState) error {
		migrateLegacy(st)
		if _, i := st.userByName(username); i >= 0 {
			st.Users[i].PasswordHash = h
			st.Users[i].Disabled = false
			return nil
		}
		created = true
		st.Users = append(st.Users, User{ID: newID(), Username: username, PasswordHash: h, Role: RoleAdmin, CreatedAt: time.Now().Unix()})
		return nil
	})
	return created, err
}

// migrateLegacy turns the v0.1 single admin password into an "admin" user.
func migrateLegacy(st *hubState) bool {
	if st.PasswordHash == "" || len(st.Users) > 0 {
		return false
	}
	st.Users = append(st.Users, User{ID: newID(), Username: "admin", PasswordHash: st.PasswordHash, Role: RoleAdmin, CreatedAt: time.Now().Unix()})
	st.PasswordHash = ""
	return true
}

// ---------- sessions ----------

type session struct {
	userID        string
	created, last time.Time
}

type sessions struct {
	mu sync.Mutex
	m  map[string]*session // key: sha256(token) hex
}

func newSessions() *sessions { return &sessions{m: map[string]*session{}} }

func tokenKey(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func (s *sessions) create(userID string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.m { // opportunistic GC
		if now.Sub(v.last) > sessionIdle || now.Sub(v.created) > sessionAbsolute {
			delete(s.m, k)
		}
	}
	s.m[tokenKey(tok)] = &session{userID: userID, created: now, last: now}
	return tok
}

func (s *sessions) lookup(tok string) (string, bool) {
	if tok == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := tokenKey(tok)
	v, ok := s.m[k]
	if !ok {
		return "", false
	}
	now := time.Now()
	if now.Sub(v.last) > sessionIdle || now.Sub(v.created) > sessionAbsolute {
		delete(s.m, k)
		return "", false
	}
	v.last = now
	return v.userID, true
}

func (s *sessions) revoke(tok string) {
	s.mu.Lock()
	delete(s.m, tokenKey(tok))
	s.mu.Unlock()
}

// revokeUser ends all sessions of a user, except the one with token keep (may be "").
func (s *sessions) revokeUser(userID, keep string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kk := ""
	if keep != "" {
		kk = tokenKey(keep)
	}
	for k, v := range s.m {
		if v.userID == userID && k != kk {
			delete(s.m, k)
		}
	}
}

// ---------- login throttling ----------

type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	max  int
	win  time.Duration
}

func newLimiter(max int, win time.Duration) *limiter {
	return &limiter{hits: map[string][]time.Time{}, max: max, win: win}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	h := l.hits[key]
	keep := h[:0]
	for _, t := range h {
		if now.Sub(t) < l.win {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.max {
		l.hits[key] = keep
		return false
	}
	l.hits[key] = append(keep, now)
	if len(l.hits) > 10000 {
		l.hits = map[string][]time.Time{}
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
