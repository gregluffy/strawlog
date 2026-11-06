package core

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"

	"strawlog/internal/api"
)

const historyKeep = 8 * 24 * time.Hour

type sampleIn struct {
	app   string
	bytes int64
}

// history stores per-source size samples, persisted as gzip JSON.
type history struct {
	path string
	mu   sync.Mutex
	Src  map[string][]api.Sample `json:"src"`
	Apps map[string]string       `json:"apps"`
}

func loadHistory(path string) *history {
	h := &history{path: path, Src: map[string][]api.Sample{}, Apps: map[string]string{}}
	f, err := os.Open(path)
	if err != nil {
		return h
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return h
	}
	var tmp history
	if json.NewDecoder(gz).Decode(&tmp) == nil && tmp.Src != nil {
		h.Src = tmp.Src
		if tmp.Apps != nil {
			h.Apps = tmp.Apps
		}
	}
	return h
}

func (h *history) add(t int64, m map[string]sampleIn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cutoff := t - int64(historyKeep.Seconds())
	for id, s := range m {
		h.Src[id] = append(h.Src[id], api.Sample{T: t, B: s.bytes})
		h.Apps[id] = s.app
	}
	for id, ss := range h.Src {
		i := 0
		for i < len(ss) && ss[i].T < cutoff {
			i++
		}
		if i == len(ss) {
			delete(h.Src, id)
			delete(h.Apps, id)
			continue
		}
		h.Src[id] = ss[i:]
	}
}

func (h *history) save() {
	h.mu.Lock()
	defer h.mu.Unlock()
	tmp := h.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	gz := gzip.NewWriter(f)
	err = json.NewEncoder(gz).Encode(h)
	gz.Close()
	f.Close()
	if err == nil {
		_ = os.Rename(tmp, h.path)
	}
}

// growth = sum of positive deltas within the window (ignores drops from rotation/cleanup).
func (h *history) growth(id string, current int64, window time.Duration) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	ss := h.Src[id]
	if len(ss) == 0 {
		return 0
	}
	cut := time.Now().Add(-window).Unix()
	var g int64
	prev := int64(-1)
	for _, s := range ss {
		if s.T < cut {
			prev = s.B
			continue
		}
		if prev >= 0 && s.B > prev {
			g += s.B - prev
		}
		prev = s.B
	}
	if prev >= 0 && current > prev {
		g += current - prev
	}
	// extrapolate when we have less than the full window
	span := time.Now().Unix() - max(ss[0].T, cut)
	if span > 3600 && span < int64(window.Seconds()) {
		g = g * int64(window.Seconds()) / span
	}
	return g
}

func (c *Core) History(ctx context.Context) (api.History, error) {
	h := c.hist
	h.mu.Lock()
	defer h.mu.Unlock()
	apps := map[string]map[int64]int64{}
	total := map[int64]int64{}
	for id, ss := range h.Src {
		app := h.Apps[id]
		if apps[app] == nil {
			apps[app] = map[int64]int64{}
		}
		for _, s := range ss {
			apps[app][s.T] += s.B
			total[s.T] += s.B
		}
	}
	out := api.History{Apps: map[string][]api.Sample{}}
	toSlice := func(m map[int64]int64) []api.Sample {
		s := make([]api.Sample, 0, len(m))
		for t, b := range m {
			s = append(s, api.Sample{T: t, B: b})
		}
		sort.Slice(s, func(i, j int) bool { return s[i].T < s[j].T })
		return s
	}
	for a, m := range apps {
		out.Apps[a] = toSlice(m)
	}
	out.Total = toSlice(total)
	return out, nil
}
