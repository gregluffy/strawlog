package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

type ctxKey struct{}

func userFrom(r *http.Request) User {
	u, _ := r.Context().Value(ctxKey{}).(User)
	return u
}

// ---------- users ----------

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	var out []User
	for _, u := range s.store.get().Users {
		out = append(out, u.public())
	}
	jsonOut(w, 200, out)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username, Password string
		Role               Role
	}
	if err := readBody(r, &req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if err := validUsername(req.Username); err != nil {
		jsonErr(w, 400, err)
		return
	}
	if !req.Role.valid() {
		jsonErr(w, 400, errors.New("role must be admin, operator or viewer"))
		return
	}
	h, err := HashPassword(req.Password)
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	u := User{ID: newID(), Username: req.Username, PasswordHash: h, Role: req.Role, CreatedAt: time.Now().Unix()}
	err = s.store.update(func(st *hubState) error {
		if _, i := st.userByName(u.Username); i >= 0 {
			return fmt.Errorf("user %q already exists", u.Username)
		}
		st.Users = append(st.Users, u)
		return nil
	})
	if err != nil {
		jsonErr(w, 409, err)
		return
	}
	s.emit(Event{Type: "security.users", Severity: "info", Title: fmt.Sprintf("User %s created (%s)", u.Username, u.Role), Fields: [][2]string{{"By", userFrom(r).Username}}})
	jsonOut(w, 200, u.public())
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role     *Role   `json:"role"`
		Password *string `json:"password"`
		Disabled *bool   `json:"disabled"`
	}
	if err := readBody(r, &req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	id := r.PathValue("id")
	me := userFrom(r)
	var hash string
	if req.Password != nil {
		h, err := HashPassword(*req.Password)
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		hash = h
	}
	var changed []string
	var updated User
	err := s.store.update(func(st *hubState) error {
		i := slices.IndexFunc(st.Users, func(u User) bool { return u.ID == id })
		if i < 0 {
			return errors.New("unknown user")
		}
		u := st.Users[i]
		if req.Role != nil && *req.Role != u.Role {
			if !req.Role.valid() {
				return errors.New("role must be admin, operator or viewer")
			}
			changed = append(changed, fmt.Sprintf("role %s → %s", u.Role, *req.Role))
			u.Role = *req.Role
		}
		if req.Disabled != nil && *req.Disabled != u.Disabled {
			if u.ID == me.ID && *req.Disabled {
				return errors.New("you can't disable yourself")
			}
			u.Disabled = *req.Disabled
			changed = append(changed, map[bool]string{true: "disabled", false: "enabled"}[u.Disabled])
		}
		if hash != "" {
			u.PasswordHash = hash
			changed = append(changed, "password reset")
		}
		st.Users[i] = u
		if st.enabledAdmins() == 0 {
			return errors.New("at least one enabled admin must remain")
		}
		updated = u
		return nil
	})
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	// role, password or status changes end the user's other sessions
	keep := ""
	if id == me.ID {
		if c, err := r.Cookie(s.cookieName()); err == nil {
			keep = c.Value
		}
	}
	s.sess.revokeUser(id, keep)
	if len(changed) > 0 {
		s.emit(Event{Type: "security.users", Severity: "info", Title: fmt.Sprintf("User %s changed: %s", updated.Username, strings.Join(changed, ", ")), Fields: [][2]string{{"By", me.Username}}})
	}
	jsonOut(w, 200, updated.public())
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	me := userFrom(r)
	if id == me.ID {
		jsonErr(w, 400, errors.New("you can't delete yourself"))
		return
	}
	var gone User
	err := s.store.update(func(st *hubState) error {
		i := slices.IndexFunc(st.Users, func(u User) bool { return u.ID == id })
		if i < 0 {
			return errors.New("unknown user")
		}
		gone = st.Users[i]
		st.Users = slices.Delete(st.Users, i, i+1)
		if st.enabledAdmins() == 0 {
			return errors.New("at least one enabled admin must remain")
		}
		return nil
	})
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	s.sess.revokeUser(id, "")
	s.emit(Event{Type: "security.users", Severity: "info", Title: fmt.Sprintf("User %s deleted", gone.Username), Fields: [][2]string{{"By", me.Username}}})
	jsonOut(w, 200, map[string]bool{"ok": true})
}

// ---------- notification channels ----------

type channelView struct {
	Channel
	URLHint string      `json:"urlHint"`
	Last    *sendResult `json:"last,omitempty"`
}

func (s *Server) channelViews() []channelView {
	out := []channelView{}
	for _, ch := range s.store.get().Channels {
		v := channelView{Channel: ch, URLHint: urlHint(ch.URL)}
		v.URL = ""
		if r, ok := s.notify.lastResult(ch.ID); ok {
			v.Last = &r
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, map[string]any{"channels": s.channelViews(), "events": eventCatalog, "publicUrl": s.notify.publicURL})
}

type channelReq struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	URL     string   `json:"url"` // empty on update = keep stored URL
	Enabled bool     `json:"enabled"`
	Events  []string `json:"events"`
	Nodes   []string `json:"nodes"`
}

// resolve validates a request and merges it with the stored channel (if any).
func (s *Server) resolveChannel(req channelReq, needName bool) (Channel, error) {
	ch := Channel{ID: req.ID, Name: strings.TrimSpace(req.Name), Type: req.Type, URL: strings.TrimSpace(req.URL), Enabled: req.Enabled, Nodes: req.Nodes}
	if ch.URL == "" && ch.ID != "" {
		for _, c := range s.store.get().Channels {
			if c.ID == ch.ID {
				if c.Type != ch.Type {
					return ch, errors.New("changing the type requires entering the webhook URL again")
				}
				ch.URL, ch.CreatedAt = c.URL, c.CreatedAt
			}
		}
	}
	if ch.URL == "" {
		return ch, errors.New("webhook URL is required")
	}
	if err := validateWebhook(ch.Type, ch.URL); err != nil {
		return ch, err
	}
	if needName && ch.Name == "" {
		return ch, errors.New("name is required")
	}
	ch.Name = trunc(ch.Name, 64)
	for _, e := range req.Events {
		if !validEvent(e) {
			return ch, fmt.Errorf("unknown event %q", e)
		}
		if !slices.Contains(ch.Events, e) {
			ch.Events = append(ch.Events, e)
		}
	}
	if ch.Events == nil {
		ch.Events = []string{}
	}
	return ch, nil
}

func (s *Server) handleSaveChannel(w http.ResponseWriter, r *http.Request) {
	var req channelReq
	if err := readBody(r, &req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	if id := r.PathValue("id"); id != "" {
		req.ID = id
	} else {
		req.ID = ""
	}
	ch, err := s.resolveChannel(req, true)
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	if len(ch.Events) == 0 {
		jsonErr(w, 400, errors.New("pick at least one notification case"))
		return
	}
	err = s.store.update(func(st *hubState) error {
		if ch.ID == "" {
			ch.ID, ch.CreatedAt = newID(), time.Now().Unix()
			st.Channels = append(st.Channels, ch)
			return nil
		}
		for i := range st.Channels {
			if st.Channels[i].ID == ch.ID {
				if ch.CreatedAt == 0 {
					ch.CreatedAt = st.Channels[i].CreatedAt
				}
				st.Channels[i] = ch
				return nil
			}
		}
		return errors.New("unknown channel")
	})
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	jsonOut(w, 200, map[string]any{"channels": s.channelViews()})
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.store.update(func(st *hubState) error {
		i := slices.IndexFunc(st.Channels, func(c Channel) bool { return c.ID == id })
		if i < 0 {
			return errors.New("unknown channel")
		}
		st.Channels = slices.Delete(st.Channels, i, i+1)
		return nil
	})
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	jsonOut(w, 200, map[string]any{"channels": s.channelViews()})
}

// handleTestChannel sends a test message to a saved or unsaved channel config.
func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	if !s.testLim.allow(userFrom(r).ID) {
		jsonErr(w, 429, errors.New("too many test messages, wait a minute"))
		return
	}
	var req channelReq
	if err := readBody(r, &req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	ch, err := s.resolveChannel(req, false)
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	if ch.ID == "" {
		ch.ID = "unsaved"
	}
	host, _ := os.Hostname()
	if s.cfg.LocalName != "" {
		host = s.cfg.LocalName
	}
	ev := Event{Type: "test", Severity: "info", Title: "Test notification from Straw Log",
		Text:   "If you can read this, Straw Log can deliver notifications to this channel.",
		Fields: [][2]string{{"Channel", firstNonEmpty(ch.Name, "(unsaved)")}, {"Sent by", userFrom(r).Username}, {"Hub", host}, {"Cases", fmt.Sprintf("%d selected", len(ch.Events))}},
		At:     time.Now()}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res := s.notify.send(ctx, ch, []Event{ev}, 1) // one attempt: immediate feedback
	jsonOut(w, 200, res)
}
