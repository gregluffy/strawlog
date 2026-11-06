package agent

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"strawlog/internal/api"
)

const maxBody = 1 << 20

var gzPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed); return w }}

func writeJSON(w http.ResponseWriter, r *http.Request, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(code)
		gz := gzPool.Get().(*gzip.Writer)
		gz.Reset(w)
		_ = json.NewEncoder(gz).Encode(v)
		gz.Close()
		gzPool.Put(gz)
		return
	}
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, r *http.Request, code int, err error) {
	writeJSON(w, r, code, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// NodeHandler exposes an api.Node over HTTP (used by the agent behind mTLS).
func NodeHandler(n api.Node) http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, r *http.Request, v any, err error) {
		if err != nil {
			writeErr(w, r, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, r, 200, v)
	}
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, r *http.Request) {
		v, err := n.Info(r.Context())
		ok(w, r, v, err)
	})
	mux.HandleFunc("GET /v1/sources", func(w http.ResponseWriter, r *http.Request) {
		v, err := n.Sources(r.Context(), r.URL.Query().Get("refresh") == "1")
		if v == nil {
			v = []api.Source{}
		}
		if err != nil && len(v) > 0 {
			err = nil // partial results (e.g. docker down) are still useful
		}
		ok(w, r, v, err)
	})
	mux.HandleFunc("POST /v1/tail", func(w http.ResponseWriter, r *http.Request) {
		var req api.TailReq
		if err := readJSON(r, &req); err != nil {
			writeErr(w, r, 400, err)
			return
		}
		v, err := n.Tail(r.Context(), req)
		if v == nil {
			v = []api.Line{}
		}
		ok(w, r, v, err)
	})
	mux.HandleFunc("POST /v1/follow", func(w http.ResponseWriter, r *http.Request) {
		var req api.TailReq
		if err := readJSON(r, &req); err != nil {
			writeErr(w, r, 400, err)
			return
		}
		StreamLines(w, r, func(emit func(api.Line) error) error { return n.Follow(r.Context(), req, emit) })
	})
	mux.HandleFunc("POST /v1/search", func(w http.ResponseWriter, r *http.Request) {
		var req api.SearchReq
		if err := readJSON(r, &req); err != nil {
			writeErr(w, r, 400, err)
			return
		}
		v, err := n.Search(r.Context(), req)
		if v.Lines == nil {
			v.Lines = []api.Line{}
		}
		ok(w, r, v, err)
	})
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		v, err := n.Health(r.Context())
		if v == nil {
			v = []api.Finding{}
		}
		ok(w, r, v, err)
	})
	mux.HandleFunc("GET /v1/history", func(w http.ResponseWriter, r *http.Request) {
		v, err := n.History(r.Context())
		ok(w, r, v, err)
	})
	mux.HandleFunc("GET /v1/policies", func(w http.ResponseWriter, r *http.Request) {
		v, err := n.Policies(r.Context())
		if v == nil {
			v = []api.Policy{}
		}
		ok(w, r, v, err)
	})
	mux.HandleFunc("PUT /v1/policies", func(w http.ResponseWriter, r *http.Request) {
		var req []api.Policy
		if err := readJSON(r, &req); err != nil {
			writeErr(w, r, 400, err)
			return
		}
		err := n.SetPolicies(r.Context(), req)
		ok(w, r, req, err)
	})
	mux.HandleFunc("POST /v1/plan", func(w http.ResponseWriter, r *http.Request) {
		var req api.PlanReq
		if err := readJSON(r, &req); err != nil {
			writeErr(w, r, 400, err)
			return
		}
		v, err := n.Plan(r.Context(), req)
		ok(w, r, v, err)
	})
	mux.HandleFunc("POST /v1/apply", func(w http.ResponseWriter, r *http.Request) {
		var req api.PlanReq
		if err := readJSON(r, &req); err != nil {
			writeErr(w, r, 400, err)
			return
		}
		v, err := n.Apply(r.Context(), req)
		ok(w, r, v, err)
	})
	mux.HandleFunc("GET /v1/runs", func(w http.ResponseWriter, r *http.Request) {
		v, err := n.Runs(r.Context())
		if v == nil {
			v = []api.Plan{}
		}
		ok(w, r, v, err)
	})
	return mux
}

// StreamLines writes NDJSON lines, flushing at most every 150ms (batches bursts).
func StreamLines(w http.ResponseWriter, r *http.Request, run func(emit func(api.Line) error) error) {
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	enc := json.NewEncoder(w)
	var mu sync.Mutex
	dirty := false
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				mu.Lock()
				if dirty && fl != nil {
					fl.Flush()
					dirty = false
				}
				mu.Unlock()
			}
		}
	}()
	if fl != nil {
		fl.Flush()
	}
	err := run(func(l api.Line) error {
		mu.Lock()
		defer mu.Unlock()
		dirty = true
		return enc.Encode(l)
	})
	close(done)
	mu.Lock()
	if err != nil && r.Context().Err() == nil {
		_ = enc.Encode(api.Line{Level: "error", Text: "stream ended: " + err.Error(), Stream: "strawlog"})
	}
	if fl != nil {
		fl.Flush()
	}
	mu.Unlock()
}
