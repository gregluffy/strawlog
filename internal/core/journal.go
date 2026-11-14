package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"strawlog/internal/api"
)

var journalLocations = []struct{ id, host string }{
	{"journal:persistent", "/var/log/journal"},
	{"journal:runtime", "/run/log/journal"},
}

func (c *Core) journalDirs() map[string]string {
	out := map[string]string{}
	for _, l := range journalLocations {
		if st, err := os.Stat(c.hp(l.host)); err == nil && st.IsDir() {
			out[l.id] = l.host
		}
	}
	return out
}

func dirSize(local string) (size int64, newest int64) {
	_ = filepath.WalkDir(local, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			size += fi.Size()
			if m := fi.ModTime().Unix(); m > newest {
				newest = m
			}
		}
		return nil
	})
	return
}

func (c *Core) journalSources() []api.Source {
	var out []api.Source
	for id, host := range c.journalDirs() {
		size, mt := dirSize(c.hp(host))
		if size == 0 {
			continue
		}
		s := api.Source{ID: id, Kind: api.KindJournal, App: "system", Name: "journald (" + strings.TrimPrefix(id, "journal:") + ")",
			Path: host, Size: size, TotalSize: size, ModTime: mt, Origin: "journal", Readable: c.cfg.Journalctl != "", Writable: c.writable(host + "/x")}
		s.Units = c.journalUnits(id, host)
		out = append(out, s)
	}
	return out
}

// unitsTTL is how long a journal's unit list is reused. Listing units makes
// journalctl read every journal file (seconds on a multi-GB journal) and the
// set of units rarely changes, so stale lists are refreshed in the background.
const unitsTTL = 30 * time.Minute

type unitsEntry struct {
	units      []string
	at         time.Time
	refreshing bool
}

// journalUnits returns the systemd units seen in a journal. Only the very first
// call per journal waits for journalctl; later ones get the cached list.
func (c *Core) journalUnits(id, host string) []string {
	if c.cfg.Journalctl == "" {
		return nil
	}
	c.unitsMu.Lock()
	if c.units == nil {
		c.units = map[string]*unitsEntry{}
	}
	e := c.units[id]
	if e == nil {
		e = &unitsEntry{}
		c.units[id] = e
		c.unitsMu.Unlock()
		units, err := c.listUnits(host)
		c.unitsMu.Lock()
		e.at = time.Now()
		if err == nil {
			e.units = units
		}
		c.unitsMu.Unlock()
		return units
	}
	if time.Since(e.at) >= unitsTTL && !e.refreshing && !e.at.IsZero() {
		e.refreshing = true
		go func() {
			units, err := c.listUnits(host)
			c.unitsMu.Lock()
			defer c.unitsMu.Unlock()
			e.refreshing, e.at = false, time.Now()
			if err == nil {
				e.units = units
			}
		}()
	}
	units := e.units
	c.unitsMu.Unlock()
	return units
}

func (c *Core) listUnits(host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t := time.Now()
	b, err := exec.CommandContext(ctx, c.cfg.Journalctl, "--directory", c.hp(host), "--no-pager", "-F", "_SYSTEMD_UNIT").Output()
	if d := time.Since(t); d > 2*time.Second {
		c.log.Info("listing journal units is slow; consider capping the journal size", "dir", host, "took", d.Round(time.Millisecond))
	}
	if err != nil {
		return nil, err
	}
	var units []string
	for _, u := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if u = strings.TrimSpace(u); u != "" {
			units = append(units, u)
		}
	}
	sort.Strings(units)
	return units, nil
}

type journalEntry struct {
	Message  json.RawMessage `json:"MESSAGE"`
	Priority string          `json:"PRIORITY"`
	Unit     string          `json:"_SYSTEMD_UNIT"`
	Ident    string          `json:"SYSLOG_IDENTIFIER"`
	TS       string          `json:"__REALTIME_TIMESTAMP"`
}

func (e journalEntry) line(src string) api.Line {
	var msg string
	if len(e.Message) > 0 && e.Message[0] == '"' {
		_ = json.Unmarshal(e.Message, &msg)
	} else if len(e.Message) > 0 && e.Message[0] == '[' {
		var bs []byte
		var ints []int
		if json.Unmarshal(e.Message, &ints) == nil {
			bs = make([]byte, len(ints))
			for i, v := range ints {
				bs[i] = byte(v)
			}
			msg = string(bs)
		}
	}
	l := api.Line{Source: src, Text: msg, Stream: firstNonEmpty(e.Ident, e.Unit)}
	if us, err := strconv.ParseInt(e.TS, 10, 64); err == nil {
		l.Time = time.UnixMicro(us).UTC().Format(time.RFC3339Nano)
	}
	switch e.Priority {
	case "0", "1", "2", "3":
		l.Level = "error"
	case "4":
		l.Level = "warn"
	case "5", "6":
		// apps writing to stdout land at info; trust an explicit level in the message
		if d := detectLevel(msg); d == "error" || d == "warn" {
			l.Level = d
		} else {
			l.Level = "info"
		}
	case "7":
		l.Level = "debug"
	default:
		l.Level = detectLevel(msg)
	}
	return l
}

func (c *Core) journalCmd(ctx context.Context, s api.Source, extra ...string) (*exec.Cmd, error) {
	if c.cfg.Journalctl == "" {
		return nil, fmt.Errorf("journalctl not available in this image")
	}
	args := []string{"--directory", c.hp(s.Path), "--no-pager", "-o", "json",
		"--output-fields=MESSAGE,PRIORITY,_SYSTEMD_UNIT,SYSLOG_IDENTIFIER"}
	args = append(args, extra...)
	return exec.CommandContext(ctx, c.cfg.Journalctl, args...), nil
}

func (c *Core) journalRun(ctx context.Context, s api.Source, emit func(api.Line) error, extra ...string) error {
	cmd, err := c.journalCmd(ctx, s, extra...)
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitWriter{w: &stderr, n: 2048}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var emitErr error
	for sc.Scan() {
		var e journalEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if emitErr = emit(e.line(s.ID)); emitErr != nil {
			break
		}
	}
	if emitErr != nil {
		_ = cmd.Process.Kill()
	}
	werr := cmd.Wait()
	if emitErr != nil {
		return emitErr
	}
	if werr != nil && ctx.Err() == nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("journalctl: %s", msg)
		}
	}
	return nil
}

func unitArgs(unit string) []string {
	if unit == "" {
		return nil
	}
	return []string{"-u", unit}
}

func (c *Core) journalTail(ctx context.Context, s api.Source, n int, unit string) ([]api.Line, error) {
	var out []api.Line
	args := append([]string{"-n", strconv.Itoa(n)}, unitArgs(unit)...)
	err := c.journalRun(ctx, s, func(l api.Line) error { out = append(out, l); return nil }, args...)
	return out, err
}

func (c *Core) journalFollow(ctx context.Context, s api.Source, n int, unit string, emit func(api.Line) error) error {
	args := append([]string{"-n", strconv.Itoa(n), "-f"}, unitArgs(unit)...)
	return c.journalRun(ctx, s, emit, args...)
}

func (c *Core) journalSearch(ctx context.Context, s api.Source, r api.SearchReq, m *matcher, res *api.SearchResult) error {
	pat := r.Query
	if !r.Regex {
		pat = regexp.QuoteMeta(r.Query)
	}
	args := []string{"-r", "-n", strconv.Itoa(r.Limit + 1)}
	if pat != "" {
		args = append(args, "--grep="+pat, "--case-sensitive="+strconv.FormatBool(r.CaseSensitive))
	}
	if t, ok := parseSince(r.Since); ok {
		args = append(args, "--since=@"+strconv.FormatInt(t.Unix(), 10))
	}
	args = append(args, unitArgs(r.Unit)...)
	var lines []api.Line
	err := c.journalRun(ctx, s, func(l api.Line) error {
		if !m.levelOK(l.Level) {
			return nil
		}
		lines = append(lines, l)
		return nil
	}, args...)
	if len(lines) > r.Limit {
		lines = lines[:r.Limit]
		res.Truncated = true
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	res.Matches += int64(len(lines))
	res.Lines = append(res.Lines, lines...)
	return err
}

func (c *Core) journalVacuum(ctx context.Context, s api.Source, sizeMB int64, days int) error {
	if c.cfg.Journalctl == "" {
		return fmt.Errorf("journalctl not available")
	}
	args := []string{"--directory", c.hp(s.Path)}
	if sizeMB > 0 {
		args = append(args, fmt.Sprintf("--vacuum-size=%dM", sizeMB))
	}
	if days > 0 {
		args = append(args, fmt.Sprintf("--vacuum-time=%dd", days))
	}
	out, err := exec.CommandContext(ctx, c.cfg.Journalctl, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

type limitWriter struct {
	w interface{ WriteString(string) (int, error) }
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.WriteString(string(p[:k]))
		l.n -= k
	}
	return len(p), nil
}

// parseSince accepts "15m", "2h", "7d" or RFC3339.
func parseSince(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil {
			return time.Now().Add(-time.Duration(n) * 24 * time.Hour), true
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}
