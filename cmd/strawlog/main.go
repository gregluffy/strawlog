// Command strawlog is a log manager: discovery, disk usage, search, live tail
// and retention for Docker, files and journald — one hub, many agents.
package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // TZ works in slim images without /usr/share/zoneinfo

	"strawlog/internal/agent"
	"strawlog/internal/api"
	"strawlog/internal/core"
	"strawlog/internal/hub"
)

const usage = `strawlog %s

Usage:
  strawlog hub                     run the dashboard (and the local node unless STRAWLOG_LOCAL=false)
  strawlog hub set-password [user] set a user's password from stdin (creates an admin if missing)
  strawlog agent                   run an agent (mTLS API for a hub)
  strawlog agent pair              print a fresh one-time pairing code
  strawlog agent hubs              list paired hubs
  strawlog agent unpair [fp]       remove all paired hubs (or the one whose fingerprint starts with fp)
  strawlog healthcheck             exit 0 if the local listener accepts connections
  strawlog version

Configuration is via environment variables, see README.md.
`

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	v := strings.ToLower(env(k, ""))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envList(k string) []string {
	var out []string
	for _, p := range strings.Split(env(k, ""), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envDur(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(env(k, "")); err == nil && d > 0 {
		return d
	}
	return def
}

func logger() *slog.Logger {
	lvl := slog.LevelInfo
	if envBool("STRAWLOG_DEBUG", false) {
		lvl = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func coreConfig(dataDir string, log *slog.Logger) core.Config {
	return core.Config{
		HostRoot:          env("STRAWLOG_HOST_ROOT", ""),
		DataDir:           dataDir,
		DockerHost:        env("DOCKER_HOST", "unix:///var/run/docker.sock"),
		DisableDocker:     !envBool("STRAWLOG_DOCKER", true),
		ExtraPaths:        envList("STRAWLOG_EXTRA_PATHS"),
		AllowWrite:        envBool("STRAWLOG_ALLOW_WRITE", false),
		WritePaths:        envList("STRAWLOG_WRITE_PATHS"),
		Journalctl:        env("STRAWLOG_JOURNALCTL", ""),
		SampleInterval:    envDur("STRAWLOG_SAMPLE_INTERVAL", 15*time.Minute),
		RetentionInterval: envDur("STRAWLOG_RETENTION_INTERVAL", time.Hour),
		Log:               log,
	}
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, usage, api.Version)
		os.Exit(2)
	}
	log := logger()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dataDir := env("STRAWLOG_DATA", "/data")

	var err error
	switch args[0] {
	case "version":
		fmt.Println(api.Version)
	case "healthcheck":
		err = healthcheck()
	case "hub":
		if len(args) > 1 && args[1] == "set-password" {
			user := "admin"
			if len(args) > 2 {
				user = args[2]
			}
			err = setPassword(dataDir, user)
			break
		}
		err = runHub(ctx, dataDir, log)
	case "agent":
		sub := ""
		if len(args) > 1 {
			sub = args[1]
		}
		switch sub {
		case "":
			err = runAgent(ctx, dataDir, log)
		case "pair":
			var code string
			if code, err = agent.NewPairingCode(dataDir); err == nil {
				fmt.Println(code)
				fmt.Fprintln(os.Stderr, "valid for 24h, single use. Paste it in the dashboard: Nodes → Add agent.")
			}
		case "hubs":
			var hubs []agent.PairedHub
			if hubs, err = agent.ListHubs(dataDir); err == nil {
				for _, h := range hubs {
					fmt.Printf("%s  %-24s  %s  paired %s\n", h.Fingerprint[:16], h.Name, h.Addr, time.Unix(h.PairedAt, 0).Format(time.RFC3339))
				}
				if len(hubs) == 0 {
					fmt.Println("no paired hubs")
				}
			}
		case "unpair":
			fp := ""
			if len(args) > 2 {
				fp = args[2]
			}
			var n int
			if n, err = agent.Unpair(dataDir, fp); err == nil {
				fmt.Printf("removed %d hub(s)\n", n)
			}
		default:
			err = fmt.Errorf("unknown agent command %q", sub)
		}
	default:
		fmt.Fprintf(os.Stderr, usage, api.Version)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runHub(ctx context.Context, dataDir string, log *slog.Logger) error {
	cfg := hub.Config{
		Listen:         env("STRAWLOG_LISTEN", ":8080"),
		DataDir:        dataDir,
		AdminUser:      env("STRAWLOG_ADMIN_USER", "admin"),
		AdminPassword:  os.Getenv("STRAWLOG_ADMIN_PASSWORD"),
		PublicURL:      env("STRAWLOG_PUBLIC_URL", ""),
		NotifyInterval: envDur("STRAWLOG_NOTIFY_INTERVAL", 5*time.Minute),
		TLS:            env("STRAWLOG_TLS", "off"),
		LocalName:      env("STRAWLOG_LOCAL_NAME", ""),
		Log:            log,
	}
	proxies, err := envPrefixes("STRAWLOG_TRUSTED_PROXIES")
	if err != nil {
		return err
	}
	cfg.TrustedProxies = proxies
	if envBool("STRAWLOG_LOCAL", true) {
		c, err := core.New(coreConfig(filepath.Join(dataDir, "local"), log))
		if err != nil {
			return err
		}
		go c.Run(ctx)
		cfg.Local = c
	}
	s, err := hub.New(cfg)
	if err != nil {
		return err
	}
	if envBool("STRAWLOG_SECURE_COOKIES", false) {
		s.SecureCookies()
	}
	return s.Run(ctx)
}

func runAgent(ctx context.Context, dataDir string, log *slog.Logger) error {
	c, err := core.New(coreConfig(dataDir, log))
	if err != nil {
		return err
	}
	allow, err := envPrefixes("STRAWLOG_ALLOW_FROM")
	if err != nil {
		return err
	}
	srv, err := agent.New(agent.Config{Listen: env("STRAWLOG_LISTEN", ":7443"), DataDir: dataDir, AllowFrom: allow, Node: c, Log: log})
	if err != nil {
		return err
	}
	go c.Run(ctx)
	return srv.Run(ctx)
}

func setPassword(dataDir, user string) error {
	fmt.Fprintf(os.Stderr, "new password for %s: ", user)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return fmt.Errorf("no input")
	}
	created, err := hub.SetPassword(dataDir, user, strings.TrimRight(sc.Text(), "\r\n"))
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(os.Stderr, "\nadmin user %q created\n", user)
	} else {
		fmt.Fprintf(os.Stderr, "\npassword for %q updated (and the account re-enabled)\n", user)
	}
	return nil
}

func healthcheck() error {
	l := env("STRAWLOG_LISTEN", "")
	if l == "" {
		l = ":8080"
		if env("STRAWLOG_MODE", "") == "agent" {
			l = ":7443"
		}
	}
	_, port, err := net.SplitHostPort(l)
	if err != nil {
		return err
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 2*time.Second)
	if err != nil {
		return err
	}
	return c.Close()
}

// envPrefixes parses a comma-separated list of IPs and CIDRs; a bare IP becomes a single-host prefix.
func envPrefixes(name string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, p := range envList(name) {
		if !strings.Contains(p, "/") {
			if a, err := netip.ParseAddr(p); err == nil {
				p = fmt.Sprintf("%s/%d", p, a.BitLen())
			}
		}
		pr, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, pr.Masked())
	}
	return out, nil
}
