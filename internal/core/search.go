package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"strawlog/internal/api"
)

const (
	maxSearchLimit   = 5000
	maxSearchSources = 500
)

type matcher struct {
	re     *regexp.Regexp
	plain  []byte
	fold   bool
	levels map[string]bool
}

func newMatcher(r api.SearchReq) (*matcher, error) {
	m := &matcher{}
	if len(r.Levels) > 0 {
		m.levels = map[string]bool{}
		for _, l := range r.Levels {
			m.levels[l] = true
		}
	}
	if r.Query == "" {
		return m, nil
	}
	if r.Regex {
		pat := r.Query
		if !r.CaseSensitive {
			pat = "(?i)" + pat
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
		m.re = re
		return m, nil
	}
	m.fold = !r.CaseSensitive
	if m.fold {
		m.plain = bytes.ToLower([]byte(r.Query))
	} else {
		m.plain = []byte(r.Query)
	}
	return m, nil
}

func (m *matcher) match(b []byte, scratch *[]byte) bool {
	switch {
	case m.re != nil:
		return m.re.Match(b)
	case m.plain == nil:
		return true
	case m.fold:
		*scratch = lowerASCII((*scratch)[:0], b)
		return bytes.Contains(*scratch, m.plain)
	default:
		return bytes.Contains(b, m.plain)
	}
}

func (m *matcher) levelOK(l string) bool {
	return m.levels == nil || m.levels[l]
}

func lowerASCII(dst, src []byte) []byte {
	for _, c := range src {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		dst = append(dst, c)
	}
	return dst
}

// ring keeps the newest n matches of a scan (circular buffer).
type ring struct {
	buf   []api.Line
	n     int
	head  int
	total int64
}

func (r *ring) push(l api.Line) {
	r.total++
	if r.n == 0 {
		return
	}
	if len(r.buf) < r.n {
		r.buf = append(r.buf, l)
		return
	}
	r.buf[r.head] = l
	r.head = (r.head + 1) % r.n
}

func (r *ring) lines() []api.Line {
	if r.head == 0 {
		return r.buf
	}
	out := make([]api.Line, 0, len(r.buf))
	out = append(out, r.buf[r.head:]...)
	return append(out, r.buf[:r.head]...)
}

func (c *Core) Search(ctx context.Context, r api.SearchReq) (api.SearchResult, error) {
	start := time.Now()
	if r.Limit <= 0 {
		r.Limit = 500
	}
	r.Limit = min(r.Limit, maxSearchLimit)
	m, err := newMatcher(r)
	if err != nil {
		return api.SearchResult{}, err
	}
	all, err := c.Sources(ctx, false)
	if err != nil && len(all) == 0 {
		return api.SearchResult{}, err
	}
	want := map[string]bool{}
	for _, s := range r.Sources {
		want[s] = true
	}
	apps := map[string]bool{}
	for _, a := range r.Apps {
		apps[a] = true
	}
	var targets []api.Source
	for _, s := range all {
		if (len(want) == 0 && len(apps) == 0) || want[s.ID] || apps[s.App] {
			if s.Readable {
				targets = append(targets, s)
			}
		}
	}
	if len(targets) > maxSearchSources {
		targets = targets[:maxSearchSources]
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var (
		mu      sync.Mutex
		res     api.SearchResult
		scanned atomic.Int64
		wg      sync.WaitGroup
		sem     = make(chan struct{}, max(2, runtime.NumCPU()))
	)
	for _, s := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(s api.Source) {
			defer wg.Done()
			defer func() { <-sem }()
			var part api.SearchResult
			var err error
			switch s.Kind {
			case api.KindJournal:
				err = c.journalSearch(ctx, s, r, m, &part)
			case api.KindDocker:
				err = c.searchDocker(ctx, s, r, m, &part, &scanned)
			default:
				err = c.searchFiles(ctx, s, r, m, &part, &scanned)
			}
			mu.Lock()
			defer mu.Unlock()
			res.Lines = append(res.Lines, part.Lines...)
			res.Matches += part.Matches
			res.Truncated = res.Truncated || part.Truncated
			if err != nil && ctx.Err() == nil {
				res.Errors = append(res.Errors, s.Name+": "+err.Error())
			}
		}(s)
	}
	wg.Wait()
	// newest first when timestamps exist, keep file order otherwise
	sort.SliceStable(res.Lines, func(i, j int) bool {
		a, b := res.Lines[i].Time, res.Lines[j].Time
		if a == "" || b == "" {
			return false
		}
		return a < b
	})
	if len(res.Lines) > r.Limit {
		res.Lines = res.Lines[len(res.Lines)-r.Limit:]
		res.Truncated = true
	}
	if res.Matches > int64(len(res.Lines)) {
		res.Truncated = true
	}
	if ctx.Err() == context.DeadlineExceeded {
		res.Errors = append(res.Errors, "search timed out after 60s; results are partial")
	}
	res.ScannedBytes = scanned.Load()
	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
}

func filesOf(s api.Source, rotated bool) []string {
	files := []string{s.Path}
	if rotated {
		for _, r := range s.Rotated {
			files = append(files, r.Path)
		}
	}
	return files
}

// searchFiles scans oldest → newest so the ring keeps the newest matches.
func (c *Core) searchFiles(ctx context.Context, s api.Source, r api.SearchReq, m *matcher, res *api.SearchResult, scanned *atomic.Int64) error {
	files := filesOf(s, r.IncludeRotated)
	rg := &ring{n: r.Limit}
	var scratch []byte
	for i := len(files) - 1; i >= 0; i-- {
		p := files[i]
		f, err := os.Open(c.hp(p))
		if err != nil {
			continue
		}
		rd, closeFn, err := openReader(f, p)
		if err != nil {
			f.Close()
			continue
		}
		label := ""
		if p != s.Path {
			label = p
		}
		err = scanLines(rd, func(b []byte) error {
			scanned.Add(int64(len(b) + 1))
			if !m.match(b, &scratch) {
				return nil
			}
			t := string(b)
			lv := detectLevel(t)
			if !m.levelOK(lv) {
				return nil
			}
			rg.push(api.Line{Source: s.ID, Text: t, Level: lv, File: label})
			return ctx.Err()
		})
		closeFn()
		f.Close()
		if err != nil {
			return err
		}
	}
	res.Lines, res.Matches = rg.lines(), rg.total
	return nil
}

func (c *Core) searchDocker(ctx context.Context, s api.Source, r api.SearchReq, m *matcher, res *api.SearchResult, scanned *atomic.Int64) error {
	rg := &ring{n: r.Limit}
	since, hasSince := parseSince(r.Since)
	sinceStr := ""
	if hasSince {
		sinceStr = since.UTC().Format(time.RFC3339Nano)
	}
	var scratch []byte
	// fast path: read json-file directly
	if s.LogDriver == "json-file" && s.Path != "" {
		if _, err := os.Stat(c.hp(s.Path)); err == nil {
			files := filesOf(s, r.IncludeRotated)
			for i := len(files) - 1; i >= 0; i-- {
				f, err := os.Open(c.hp(files[i]))
				if err != nil {
					continue
				}
				rd, closeFn, err := openReader(f, files[i])
				if err != nil {
					f.Close()
					continue
				}
				err = scanLines(rd, func(b []byte) error {
					scanned.Add(int64(len(b) + 1))
					// cheap pre-filter on the raw JSON, then decode
					if m.re == nil && m.plain != nil && !m.match(b, &scratch) {
						return nil
					}
					l, ok := parseDockerLine(b)
					if !ok {
						return nil
					}
					if sinceStr != "" && l.Time < sinceStr {
						return nil
					}
					if !m.match([]byte(l.Text), &scratch) || !m.levelOK(l.Level) {
						return nil
					}
					l.Source = s.ID
					rg.push(l)
					return ctx.Err()
				})
				closeFn()
				f.Close()
				if err != nil {
					return err
				}
			}
			res.Lines, res.Matches = rg.lines(), rg.total
			return nil
		}
	}
	// slow path: stream through the API
	_, cj, err := c.source(s.ID)
	if err != nil {
		return err
	}
	apiSince := ""
	if hasSince {
		apiSince = strconv.FormatInt(since.Unix(), 10)
	}
	err = c.docker.Logs(ctx, cj.ID, cj.Config.Tty, -1, false, apiSince, func(st, ts, text string) error {
		scanned.Add(int64(len(text) + 1))
		b := []byte(text)
		if !m.match(b, &scratch) {
			return nil
		}
		lv := detectLevel(text)
		if !m.levelOK(lv) {
			return nil
		}
		rg.push(api.Line{Source: s.ID, Time: ts, Stream: st, Text: cut(text), Level: lv})
		return ctx.Err()
	})
	res.Lines, res.Matches = rg.lines(), rg.total
	return err
}
