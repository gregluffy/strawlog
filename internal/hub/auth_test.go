package hub

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"strawlog/internal/api"
)

type apiFinding = api.Finding

func TestPassword(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !checkPassword(h, "correct horse battery") || checkPassword(h, "wrong password!!") {
		t.Fatal("password check broken")
	}
}

func TestNormalizeAddr(t *testing.T) {
	cases := map[string]string{"10.0.0.5": "10.0.0.5:7443", "https://vm.lan:9000/": "vm.lan:9000", "[fd00::1]": "[fd00::1]:7443"}
	for in, want := range cases {
		if got, err := normalizeAddr(in); err != nil || got != want {
			t.Errorf("normalizeAddr(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizeAddr("host:99999"); err == nil {
		t.Error("bad port accepted")
	}
}

func TestStoreMigrationAndCLI(t *testing.T) {
	dir := t.TempDir()
	if _, err := SetPassword(dir, "root", "a-long-password"); err != nil {
		t.Fatal(err)
	}
	s, _ := openStore(dir)
	st := s.get()
	if len(st.Users) != 1 || st.Users[0].Role != RoleAdmin {
		t.Fatalf("users: %+v", st.Users)
	}
	// legacy single password migrates to an admin user
	st2 := hubState{PasswordHash: "x"}
	if !migrateLegacy(&st2) || st2.Users[0].Username != "admin" || st2.PasswordHash != "" {
		t.Fatalf("migration: %+v", st2)
	}
}

func TestSessionsRevokeUser(t *testing.T) {
	s := newSessions()
	a := s.create("u1")
	b := s.create("u1")
	c := s.create("u2")
	s.revokeUser("u1", a)
	if _, ok := s.lookup(a); !ok {
		t.Error("kept session revoked")
	}
	if _, ok := s.lookup(b); ok {
		t.Error("other session not revoked")
	}
	if _, ok := s.lookup(c); !ok {
		t.Error("foreign session revoked")
	}
}

func TestWebhookValidation(t *testing.T) {
	ok := [][2]string{{ChanDiscord, "https://discord.com/api/webhooks/1/abc"}, {ChanTeams, "https://x.webhook.office.com/webhookb2/abc"}, {ChanGeneric, "http://n8n.lan/hook"}}
	bad := [][2]string{{ChanDiscord, "https://evil.com/api/webhooks/1"}, {ChanDiscord, "http://discord.com/api/webhooks/1"}, {ChanTeams, "http://x"}, {ChanGeneric, "ftp://x"}, {ChanGeneric, "https://user:pw@x"}}
	for _, c := range ok {
		if err := validateWebhook(c[0], c[1]); err != nil {
			t.Errorf("%v rejected: %v", c, err)
		}
	}
	for _, c := range bad {
		if validateWebhook(c[0], c[1]) == nil {
			t.Errorf("%v accepted", c)
		}
	}
}

func TestDiffFindings(t *testing.T) {
	st := &monitorState{Findings: map[string]map[string]findingState{}, LastRun: map[string]int64{}}
	f1 := []apiFinding{{Key: "a", Severity: "critical", Title: "A"}}
	if evs := diffFindings(st, "n", "node", f1, true); len(evs) != 0 {
		t.Fatal("baseline should be silent")
	}
	f2 := []apiFinding{{Key: "a", Severity: "critical", Title: "A"}, {Key: "b", Severity: "warning", Title: "B"}}
	evs := diffFindings(st, "n", "node", f2, false)
	if len(evs) != 1 || evs[0].Type != "health.warning" {
		t.Fatalf("new finding: %+v", evs)
	}
	evs = diffFindings(st, "n", "node", nil, false)
	if len(evs) != 2 || evs[0].Type != "health.resolved" {
		t.Fatalf("resolved: %+v", evs)
	}
	_ = time.Now
}

func TestFailedLogins(t *testing.T) {
	var f failedLogins
	n := 0
	for i := 0; i < 12; i++ {
		if _, ok := f.record("1.2.3.4", "admin"); ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one alert, got %d", n)
	}
}

func TestClientIPTrustedProxies(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("10.0.0.0/8")}
	cases := []struct {
		name    string
		proxies []netip.Prefix
		remote  string
		xff     []string
		want    string
	}{
		{"no proxies configured ignores header", nil, "127.0.0.1:5000", []string{"203.0.113.9"}, "127.0.0.1"},
		{"untrusted peer cannot spoof", trusted, "198.51.100.7:5000", []string{"203.0.113.9"}, "198.51.100.7"},
		{"trusted proxy, single hop", trusted, "127.0.0.1:5000", []string{"203.0.113.9"}, "203.0.113.9"},
		{"client-supplied prefix is skipped", trusted, "127.0.0.1:5000", []string{"1.2.3.4, 203.0.113.9"}, "203.0.113.9"},
		{"chained trusted proxies", trusted, "127.0.0.1:5000", []string{"203.0.113.9, 10.1.2.3"}, "203.0.113.9"},
		{"multiple header lines", trusted, "127.0.0.1:5000", []string{"203.0.113.9", "10.1.2.3"}, "203.0.113.9"},
		{"no header", trusted, "127.0.0.1:5000", nil, "127.0.0.1"},
		{"malformed hop falls back to peer", trusted, "127.0.0.1:5000", []string{"203.0.113.9, garbage"}, "127.0.0.1"},
		{"malformed hop behind trusted hop", trusted, "127.0.0.1:5000", []string{"garbage, 10.1.2.3"}, "10.1.2.3"},
		{"all hops trusted returns left-most", trusted, "127.0.0.1:5000", []string{"10.9.9.9, 10.1.2.3"}, "10.9.9.9"},
		{"ipv4-mapped peer", trusted, "[::ffff:127.0.0.1]:5000", []string{"203.0.113.9"}, "203.0.113.9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{cfg: Config{TrustedProxies: c.proxies}}
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			for _, h := range c.xff {
				r.Header.Add("X-Forwarded-For", h)
			}
			if got := s.clientIP(r); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
