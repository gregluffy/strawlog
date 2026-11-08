package core

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"strawlog/internal/api"
)

func TestBaseOf(t *testing.T) {
	cases := map[string]string{
		"syslog":              "syslog",
		"syslog.1":            "syslog",
		"syslog.2.gz":         "syslog",
		"access.log":          "access.log",
		"access.log.1":        "access.log",
		"access.log.14.gz":    "access.log",
		"app.log-20261101":    "app.log",
		"app.log-20261101.gz": "app.log",
		"app-2026-11-01.log":  "app-2026-11-01.log",
		"error.log.1.zst":     "error.log",
	}
	for in, want := range cases {
		if got := baseOf(in); got != want {
			t.Errorf("baseOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectLevel(t *testing.T) {
	cases := map[string]string{
		"2026-01-01 ERROR boom":          "error",
		`level=warning msg="x"`:          "warn",
		"[INFO] started":                 "info",
		"DEBUG cache miss":               "debug",
		"nothing here":                   "",
		"panic: runtime error":           "error",
		"Information: not a level token": "",
	}
	for in, want := range cases {
		if got := detectLevel(in); got != want {
			t.Errorf("detectLevel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTailFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.log")
	var b []byte
	for i := 0; i < 100000; i++ {
		b = append(b, []byte("line number something something\n")...)
	}
	b = append(b, []byte("last-1\nlast\n")...)
	os.WriteFile(p, b, 0o644)
	got, err := tailFile(p, 2)
	if err != nil || len(got) != 2 || got[0] != "last-1" || got[1] != "last" {
		t.Fatalf("tail = %v, %v", got, err)
	}
}

func newTestCore(t *testing.T, root string, write bool) *Core {
	c, err := New(Config{HostRoot: root, DataDir: t.TempDir(), DisableDocker: true, AllowWrite: write, WritePaths: []string{"/var/log"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDiscoveryAndRetention(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, "var/log/nginx")
	os.MkdirAll(logs, 0o755)
	write := func(name string, size int, age time.Duration) {
		p := filepath.Join(logs, name)
		os.WriteFile(p, make([]byte, size), 0o644)
		mt := time.Now().Add(-age)
		os.Chtimes(p, mt, mt)
	}
	write("access.log", 3*mb, 0)
	write("access.log.1", 1000, 2*time.Hour)
	write("access.log.2.gz", 1000, 3*24*time.Hour)
	write("access.log.3.gz", 1000, 30*24*time.Hour)
	os.WriteFile(filepath.Join(root, "var/log/wtmp"), []byte("bin"), 0o644)

	c := newTestCore(t, root, true)
	srcs, err := c.Sources(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	var nginx *api.Source
	for i := range srcs {
		if srcs[i].Name == "wtmp" {
			t.Error("binary wtmp should be skipped")
		}
		if srcs[i].ID == "file:/var/log/nginx/access.log" {
			nginx = &srcs[i]
		}
	}
	if nginx == nil || nginx.App != "nginx" || len(nginx.Rotated) != 3 || !nginx.Writable {
		t.Fatalf("nginx source wrong: %+v", nginx)
	}

	pol := api.Policy{Name: "t", Enabled: true, Match: api.Match{Apps: []string{"nginx"}}, MaxAgeDays: 7, MaxActiveMB: 2, CompressRotated: true}
	plan, err := c.Plan(t.Context(), api.PlanReq{Policy: &pol})
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]string{}
	for _, a := range plan.Actions {
		ops[filepath.Base(a.Path)] = a.Op
	}
	want := map[string]string{"access.log.3.gz": "delete", "access.log.1": "compress", "access.log": "truncate"}
	for k, v := range want {
		if ops[k] != v {
			t.Errorf("op for %s = %q, want %q (all: %v)", k, ops[k], v, ops)
		}
	}
	if err := c.SetPolicies(t.Context(), []api.Policy{pol}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Apply(t.Context(), api.PlanReq{}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(logs, "access.log")); fi == nil || fi.Size() != 0 {
		t.Error("active file should be truncated, not removed")
	}
	if _, err := os.Stat(filepath.Join(logs, "access.log.3.gz")); !os.IsNotExist(err) {
		t.Error("old rotation should be deleted")
	}
	if _, err := os.Stat(filepath.Join(logs, "access.log.1.gz")); err != nil {
		t.Error("rotation should be compressed")
	}
}

func TestReadOnlyNodeRefusesApply(t *testing.T) {
	c := newTestCore(t, t.TempDir(), false)
	if _, err := c.Apply(t.Context(), api.PlanReq{}); err == nil {
		t.Fatal("expected read-only error")
	}
}

func TestSearchRing(t *testing.T) {
	r := &ring{n: 3}
	for i := 0; i < 10; i++ {
		r.push(api.Line{Text: string(rune('a' + i))})
	}
	got := ""
	for _, l := range r.lines() {
		got += l.Text
	}
	if got != "hij" || r.total != 10 {
		t.Fatalf("ring = %q total %d", got, r.total)
	}
}

func TestSearchFilesSkipsStaleFiles(t *testing.T) {
	dir := t.TempDir()
	cur, old := filepath.Join(dir, "app.log"), filepath.Join(dir, "app.log.1")
	os.WriteFile(cur, []byte("hit new\n"), 0o644)
	os.WriteFile(old, []byte("hit old\n"), 0o644)
	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(old, past, past)
	c := &Core{}
	s := api.Source{ID: "x", Path: cur, Rotated: []api.FileInfo{{Path: old}}}
	run := func(since string) []api.Line {
		r := api.SearchReq{Query: "hit", IncludeRotated: true, Since: since, Limit: 10}
		m, _ := newMatcher(r)
		var res api.SearchResult
		var n atomic.Int64
		if err := c.searchFiles(context.Background(), s, r, m, &res, &n); err != nil {
			t.Fatal(err)
		}
		return res.Lines
	}
	if got := run(""); len(got) != 2 {
		t.Fatalf("no since: got %d lines, want 2", len(got))
	}
	if got := run("24h"); len(got) != 1 || got[0].Text != "hit new" {
		t.Fatalf("since 24h: got %v, want only the fresh file", got)
	}
}
