package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"strawlog/internal/api"
)

// ---------- model ----------

const (
	ChanDiscord = "discord"
	ChanTeams   = "teams"
	ChanGeneric = "generic"
)

type Channel struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	URL       string   `json:"url,omitempty"` // secret: never sent back to the browser
	Enabled   bool     `json:"enabled"`
	Events    []string `json:"events"`
	Nodes     []string `json:"nodes,omitempty"` // empty = all nodes
	CreatedAt int64    `json:"createdAt"`
}

type EventInfo struct {
	ID          string `json:"id"`
	Group       string `json:"group"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Default     bool   `json:"default"`
}

var eventCatalog = []EventInfo{
	{"node.down", "Nodes", "Node offline", "An agent stopped answering (2 failed checks in a row).", true},
	{"node.up", "Nodes", "Node back online", "A node that was offline answers again.", true},
	{"health.critical", "Health", "New critical problem", "e.g. disk ≥ 90%, a log over 5 GB, unbounded container logs over 1 GB.", true},
	{"health.warning", "Health", "New warning", "e.g. disk ≥ 80%, container without max-size, file never rotated, DEBUG logging, fast growth.", false},
	{"health.resolved", "Health", "Problem resolved", "A critical problem or warning went away.", false},
	{"retention.applied", "Retention", "Retention freed space", "A policy run (scheduled or manual) deleted, compressed or truncated files.", false},
	{"retention.failed", "Retention", "Retention action failed", "A policy run could not delete, compress or truncate something.", true},
	{"security.login", "Security", "Failed logins", "5 or more failed dashboard logins within 5 minutes (at most every 30 min).", true},
	{"security.users", "Security", "User accounts changed", "A user was created, deleted, disabled or changed role.", false},
}

func validEvent(id string) bool {
	for _, e := range eventCatalog {
		if e.ID == id {
			return true
		}
	}
	return false
}

type Event struct {
	Type     string      `json:"type"`
	Severity string      `json:"severity"` // critical, warning, info, ok
	Node     string      `json:"node,omitempty"`
	NodeName string      `json:"nodeName,omitempty"`
	Title    string      `json:"title"`
	Text     string      `json:"text,omitempty"`
	Fields   [][2]string `json:"fields,omitempty"`
	At       time.Time   `json:"at"`
}

// ---------- URL validation (admins only set these, but keep the hub from being an open relay) ----------

func validateWebhook(typ, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return errors.New("invalid webhook URL")
	}
	if u.User != nil {
		return errors.New("credentials in the URL are not allowed")
	}
	switch typ {
	case ChanDiscord:
		h := strings.ToLower(u.Hostname())
		if u.Scheme != "https" || !(h == "discord.com" || h == "discordapp.com" || h == "ptb.discord.com" || h == "canary.discord.com") || !strings.HasPrefix(u.Path, "/api/webhooks/") {
			return errors.New("expected a Discord webhook URL like https://discord.com/api/webhooks/<id>/<token>")
		}
	case ChanTeams:
		if u.Scheme != "https" {
			return errors.New("Teams webhook URL must use https")
		}
	case ChanGeneric:
		if u.Scheme != "https" && u.Scheme != "http" {
			return errors.New("webhook URL must be http or https")
		}
	default:
		return fmt.Errorf("unknown channel type %q", typ)
	}
	return nil
}

func urlHint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := u.Path
	if len(p) > 18 {
		p = p[:18] + "…"
	}
	return u.Scheme + "://" + u.Host + p
}

// ---------- payloads ----------

var sevColor = map[string]int{"critical": 0xEF5F67, "warning": 0xE8A33D, "info": 0x5AA8F0, "ok": 0x4CC38A}
var sevTeams = map[string]string{"critical": "Attention", "warning": "Warning", "info": "Accent", "ok": "Good"}
var sevIcon = map[string]string{"critical": "🔴", "warning": "🟠", "info": "🔵", "ok": "🟢"}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func eventTitle(e Event) string {
	if e.NodeName != "" {
		return fmt.Sprintf("[%s] %s", e.NodeName, e.Title)
	}
	return e.Title
}

func discordPayload(evs []Event, publicURL string) any {
	type field struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Inline bool   `json:"inline"`
	}
	type embed struct {
		Title       string  `json:"title"`
		Description string  `json:"description,omitempty"`
		URL         string  `json:"url,omitempty"`
		Color       int     `json:"color"`
		Fields      []field `json:"fields,omitempty"`
		Timestamp   string  `json:"timestamp"`
		Footer      struct {
			Text string `json:"text"`
		} `json:"footer"`
	}
	var embeds []embed
	for _, e := range evs {
		em := embed{Title: clip(sevIcon[e.Severity]+" "+eventTitle(e), 256), Description: clip(e.Text, 3000), Color: sevColor[e.Severity], Timestamp: e.At.UTC().Format(time.RFC3339), URL: publicURL}
		em.Footer.Text = "Straw Log · " + e.Type
		for i, f := range e.Fields {
			if i == 10 {
				break
			}
			em.Fields = append(em.Fields, field{Name: clip(f[0], 256), Value: clip(f[1], 1000), Inline: len(f[1]) < 40})
		}
		embeds = append(embeds, em)
	}
	return map[string]any{"username": "Straw Log", "embeds": embeds, "allowed_mentions": map[string]any{"parse": []string{}}}
}

// teamsPayload builds an Adaptive Card message; accepted by Teams Workflows ("When a Teams webhook
// request is received") and by legacy Incoming Webhook connectors.
func teamsPayload(evs []Event, publicURL string) any {
	var body []any
	for i, e := range evs {
		items := []any{
			map[string]any{"type": "TextBlock", "text": sevIcon[e.Severity] + " " + eventTitle(e), "weight": "Bolder", "size": "Medium", "color": sevTeams[e.Severity], "wrap": true},
		}
		if e.Text != "" {
			items = append(items, map[string]any{"type": "TextBlock", "text": clip(e.Text, 2000), "wrap": true, "spacing": "Small"})
		}
		facts := []any{}
		for _, f := range e.Fields {
			facts = append(facts, map[string]string{"title": f[0], "value": clip(f[1], 500)})
		}
		facts = append(facts, map[string]string{"title": "Event", "value": e.Type}, map[string]string{"title": "Time", "value": e.At.UTC().Format("2006-01-02 15:04:05 UTC")})
		items = append(items, map[string]any{"type": "FactSet", "facts": facts})
		c := map[string]any{"type": "Container", "items": items}
		if i > 0 {
			c["separator"] = true
			c["spacing"] = "Medium"
		}
		body = append(body, c)
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"msteams": map[string]any{"width": "Full"},
		"body":    body,
	}
	if publicURL != "" {
		card["actions"] = []any{map[string]any{"type": "Action.OpenUrl", "title": "Open Straw Log", "url": publicURL}}
	}
	return map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"contentUrl":  nil,
			"content":     card,
		}},
	}
}

func genericPayload(evs []Event, publicURL string) any {
	return map[string]any{"source": "strawlog", "version": api.Version, "dashboard": publicURL, "events": evs}
}

// ---------- delivery ----------

type sendResult struct {
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	At     int64  `json:"at"`
}

type notifier struct {
	hc        *http.Client
	publicURL string
	mu        sync.Mutex
	last      map[string]sendResult // by channel id
}

func newNotifier(publicURL string) *notifier {
	return &notifier{
		publicURL: publicURL,
		last:      map[string]sendResult{},
		hc: &http.Client{
			Timeout: 10 * time.Second,
			// never follow redirects: a webhook must answer directly
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (n *notifier) payload(ch Channel, evs []Event) any {
	switch ch.Type {
	case ChanDiscord:
		return discordPayload(evs, n.publicURL)
	case ChanTeams:
		return teamsPayload(evs, n.publicURL)
	default:
		return genericPayload(evs, n.publicURL)
	}
}

// send delivers events (chunked) to one channel with retries on 429/5xx.
func (n *notifier) send(ctx context.Context, ch Channel, evs []Event, attempts int) sendResult {
	var res sendResult
	for len(evs) > 0 {
		chunk := evs[:min(len(evs), 10)]
		evs = evs[len(chunk):]
		res = n.post(ctx, ch, chunk, attempts)
		if !res.OK {
			break
		}
	}
	res.At = time.Now().Unix()
	n.mu.Lock()
	n.last[ch.ID] = res
	n.mu.Unlock()
	return res
}

func (n *notifier) post(ctx context.Context, ch Channel, evs []Event, attempts int) sendResult {
	body, err := json.Marshal(n.payload(ch, evs))
	if err != nil {
		return sendResult{Error: err.Error()}
	}
	var last sendResult
	for attempt := 0; attempt < attempts; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "POST", ch.URL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "strawlog/"+api.Version)
		resp, err := n.hc.Do(req)
		if err != nil {
			last = sendResult{Error: scrubURL(err.Error(), ch.URL)}
			if attempt+1 < attempts {
				sleepCtx(ctx, time.Duration(attempt+1)*2*time.Second)
			}
			continue
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		last = sendResult{Status: resp.StatusCode, OK: resp.StatusCode >= 200 && resp.StatusCode < 300}
		if last.OK {
			return last
		}
		last.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, clip(strings.TrimSpace(string(msg)), 300))
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			wait := time.Duration(attempt+1) * 2 * time.Second
			if ra, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil && ra > 0 && ra < 60 {
				wait = time.Duration(ra * float64(time.Second))
			}
			if attempt+1 < attempts {
				sleepCtx(ctx, wait)
			}
			continue
		}
		return last // 4xx other than 429: config problem, don't retry
	}
	return last
}

func scrubURL(msg, u string) string { return strings.ReplaceAll(msg, u, urlHint(u)) }

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (n *notifier) lastResult(id string) (sendResult, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	r, ok := n.last[id]
	return r, ok
}

// dispatch routes events to every enabled channel that subscribed to them.
func (s *Server) dispatch(evs []Event) {
	if len(evs) == 0 {
		return
	}
	for _, ch := range s.store.get().Channels {
		if !ch.Enabled {
			continue
		}
		var mine []Event
		for _, e := range evs {
			if !slices.Contains(ch.Events, e.Type) {
				continue
			}
			if e.Node != "" && len(ch.Nodes) > 0 && !slices.Contains(ch.Nodes, e.Node) {
				continue
			}
			mine = append(mine, e)
		}
		if len(mine) == 0 {
			continue
		}
		go func(ch Channel, mine []Event) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if r := s.notify.send(ctx, ch, mine, 3); !r.OK {
				s.log.Warn("notification failed", "channel", ch.Name, "err", r.Error)
			} else {
				s.log.Info("notification sent", "channel", ch.Name, "events", len(mine))
			}
		}(ch, mine)
	}
}

// emit queues an immediate event (security events) without blocking the request.
func (s *Server) emit(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	select {
	case s.events <- e:
	default:
		s.log.Warn("notification queue full; dropping event", "type", e.Type)
	}
}

// ---------- monitor ----------

type findingState struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
}

type monitorState struct {
	Initialized map[string]bool                    `json:"initialized"` // per node
	Findings    map[string]map[string]findingState `json:"findings"`    // node -> key -> state
	Fails       map[string]int                     `json:"fails"`
	Down        map[string]bool                    `json:"down"`
	LastRun     map[string]int64                   `json:"lastRun"`
}

func (s *Server) monitorStatePath() string { return filepath.Join(s.cfg.DataDir, "notify-state.json") }

func (s *Server) loadMonitorState() *monitorState {
	st := &monitorState{}
	if b, err := os.ReadFile(s.monitorStatePath()); err == nil {
		_ = json.Unmarshal(b, st)
	}
	if st.Initialized == nil {
		st.Initialized = map[string]bool{}
	}
	if st.Findings == nil {
		st.Findings = map[string]map[string]findingState{}
	}
	if st.Fails == nil {
		st.Fails = map[string]int{}
	}
	if st.Down == nil {
		st.Down = map[string]bool{}
	}
	if st.LastRun == nil {
		st.LastRun = map[string]int64{}
	}
	return st
}

func (s *Server) saveMonitorState(st *monitorState) {
	b, _ := json.Marshal(st)
	tmp := s.monitorStatePath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.monitorStatePath())
	}
}

// monitor checks every node periodically and turns state changes into events.
// The first check of a node only records a baseline, so enabling notifications
// doesn't flood channels with everything that was already wrong.
func (s *Server) monitor(ctx context.Context, interval time.Duration) {
	st := s.loadMonitorState()
	t := time.NewTimer(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-s.events:
			batch := []Event{e}
		drain:
			for {
				select {
				case e := <-s.events:
					batch = append(batch, e)
				default:
					break drain
				}
			}
			s.dispatch(batch)
		case <-t.C:
			s.dispatch(s.checkNodes(ctx, st))
			s.saveMonitorState(st)
			t.Reset(interval)
		}
	}
}

func (s *Server) checkNodes(ctx context.Context, st *monitorState) []Event {
	views := s.nodeList()
	s.probe(ctx, views)
	var (
		mu  sync.Mutex
		evs []Event
		wg  sync.WaitGroup
	)
	known := map[string]bool{}
	for _, v := range views {
		known[v.ID] = true
	}
	for id := range st.Initialized { // forget removed nodes
		if !known[id] {
			delete(st.Initialized, id)
			delete(st.Findings, id)
			delete(st.Fails, id)
			delete(st.Down, id)
			delete(st.LastRun, id)
		}
	}
	for _, v := range views {
		name := v.Name
		if name == "" && v.Info != nil {
			name = v.Info.Hostname
		}
		if !v.Online {
			st.Fails[v.ID]++
			if st.Fails[v.ID] == 2 && !st.Down[v.ID] {
				st.Down[v.ID] = true
				evs = append(evs, Event{Type: "node.down", Severity: "critical", Node: v.ID, NodeName: name, Title: "Node is offline",
					Text: v.Error, Fields: [][2]string{{"Address", firstNonEmpty(v.Addr, "local")}}, At: time.Now()})
			}
			continue
		}
		if st.Down[v.ID] {
			evs = append(evs, Event{Type: "node.up", Severity: "ok", Node: v.ID, NodeName: name, Title: "Node is back online", Fields: [][2]string{{"Latency", fmt.Sprintf("%d ms", v.LatencyMs)}}, At: time.Now()})
		}
		st.Fails[v.ID], st.Down[v.ID] = 0, false

		wg.Add(1)
		go func(id, name string) {
			defer wg.Done()
			nd, err := s.node(id)
			if err != nil {
				return
			}
			c, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			findings, ferr := nd.Health(c)
			runs, rerr := nd.Runs(c)
			mu.Lock()
			defer mu.Unlock()
			first := !st.Initialized[id]
			if ferr == nil {
				evs = append(evs, diffFindings(st, id, name, findings, first)...)
			}
			if rerr == nil {
				evs = append(evs, diffRuns(st, id, name, runs, first)...)
			}
			if ferr == nil && rerr == nil {
				st.Initialized[id] = true
			}
		}(v.ID, name)
	}
	wg.Wait()
	return evs
}

func diffFindings(st *monitorState, node, name string, findings []api.Finding, baseline bool) []Event {
	prev := st.Findings[node]
	cur := map[string]findingState{}
	var evs []Event
	now := time.Now()
	for _, f := range findings {
		if f.Severity != "critical" && f.Severity != "warning" {
			continue
		}
		key := f.Key
		if key == "" {
			key = f.Code + "|" + f.SourceID
		}
		cur[key] = findingState{Severity: f.Severity, Title: f.Title}
		if _, seen := prev[key]; seen || baseline {
			continue
		}
		e := Event{Type: "health." + f.Severity, Severity: f.Severity, Node: node, NodeName: name, Title: f.Title, Text: f.Detail, At: now}
		if f.App != "" {
			e.Fields = append(e.Fields, [2]string{"App", f.App})
		}
		if f.Fix != "" {
			e.Fields = append(e.Fields, [2]string{"How to fix", clip(f.Fix, 900)})
		}
		evs = append(evs, e)
	}
	if !baseline {
		for key, p := range prev {
			if _, still := cur[key]; !still {
				evs = append(evs, Event{Type: "health.resolved", Severity: "ok", Node: node, NodeName: name, Title: "Resolved: " + p.Title, At: now})
			}
		}
	}
	st.Findings[node] = cur
	return evs
}

func diffRuns(st *monitorState, node, name string, runs []api.Plan, baseline bool) []Event {
	last := st.LastRun[node]
	newest := last
	var evs []Event
	for _, r := range runs {
		if r.At > newest {
			newest = r.At
		}
		if baseline || r.At <= last {
			continue
		}
		done, failed := 0, 0
		var errs []string
		for _, a := range r.Actions {
			if a.Done {
				done++
			}
			if a.Error != "" {
				failed++
				if len(errs) < 5 {
					errs = append(errs, fmt.Sprintf("%s %s: %s", a.Op, a.Path, a.Error))
				}
			}
		}
		at := time.Unix(r.At, 0)
		if done > 0 {
			evs = append(evs, Event{Type: "retention.applied", Severity: "info", Node: node, NodeName: name,
				Title:  fmt.Sprintf("Retention freed %s", humanBytes(r.FreedBytes)),
				Fields: [][2]string{{"Actions", strconv.Itoa(done)}, {"Trigger", r.Trigger}}, At: at})
		}
		if failed > 0 {
			evs = append(evs, Event{Type: "retention.failed", Severity: "warning", Node: node, NodeName: name,
				Title: fmt.Sprintf("%d retention action(s) failed", failed), Text: strings.Join(errs, "\n"),
				Fields: [][2]string{{"Trigger", r.Trigger}}, At: at})
		}
	}
	st.LastRun[node] = newest
	return evs
}

func humanBytes(b int64) string {
	const k = 1024
	switch {
	case b >= k*k*k:
		return fmt.Sprintf("%.1f GB", float64(b)/(k*k*k))
	case b >= k*k:
		return fmt.Sprintf("%.1f MB", float64(b)/(k*k))
	case b >= k:
		return fmt.Sprintf("%.1f KB", float64(b)/k)
	}
	return fmt.Sprintf("%d B", b)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// failedLogins aggregates failed logins into at most one security.login event per 30 min.
type failedLogins struct {
	mu        sync.Mutex
	hits      []time.Time
	ips       map[string]int
	users     map[string]int
	lastAlert time.Time
}

func (f *failedLogins) record(ip, user string) (Event, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	keep := f.hits[:0]
	for _, t := range f.hits {
		if now.Sub(t) < 5*time.Minute {
			keep = append(keep, t)
		}
	}
	f.hits = append(keep, now)
	if f.ips == nil || len(keep) == 0 {
		f.ips, f.users = map[string]int{}, map[string]int{}
	}
	f.ips[ip]++
	f.users[user]++
	if len(f.hits) < 5 || now.Sub(f.lastAlert) < 30*time.Minute {
		return Event{}, false
	}
	f.lastAlert = now
	top := func(m map[string]int) string {
		var parts []string
		for k, v := range m {
			if len(parts) == 8 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s (%d)", k, v))
		}
		slices.Sort(parts)
		return strings.Join(parts, ", ")
	}
	e := Event{Type: "security.login", Severity: "warning", Title: fmt.Sprintf("%d failed dashboard logins in 5 minutes", len(f.hits)),
		Fields: [][2]string{{"From", top(f.ips)}, {"Usernames", top(f.users)}}, At: now}
	f.ips, f.users = map[string]int{}, map[string]int{}
	return e, true
}
