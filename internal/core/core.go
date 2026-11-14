// Package core discovers, reads, searches and cleans log sources on the
// machine it runs on. It implements api.Node in-process; the agent exposes
// it over mTLS and the hub can embed it as the "local" node.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"strawlog/internal/api"
)

type Config struct {
	HostRoot          string // where the host "/" is mounted ("" when running natively)
	DataDir           string // state: policies, history
	DockerHost        string // unix:///var/run/docker.sock or tcp://socket-proxy:2375
	DisableDocker     bool
	ExtraPaths        []string      // "app=/host/path/*.log" or "/host/path"
	AllowWrite        bool          // master switch for retention actions
	WritePaths        []string      // host path prefixes retention may touch
	Journalctl        string        // path to journalctl binary
	SampleInterval    time.Duration // size history sampling
	RetentionInterval time.Duration // default interval for policies without an explicit schedule
	Log               *slog.Logger
}

type Core struct {
	cfg     Config
	docker  *Docker
	started time.Time
	log     *slog.Logger

	scanMu     sync.Mutex  // serialises scans
	refreshing atomic.Bool // a background rescan started by cachedSources is running
	mu         sync.RWMutex
	sources    []api.Source
	byID       map[string]*api.Source
	containers map[string]ContainerJSON // by source id
	lastScan   time.Time
	dockerErr  string

	unitsMu   sync.Mutex
	units     map[string][]string
	unitsTime time.Time

	hist     *history
	policies *policyStore
	sched    *runState
	runsMu   sync.Mutex
	runs     []api.Plan
	applyMu  sync.Mutex
}

func New(cfg Config) (*Core, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.SampleInterval <= 0 {
		cfg.SampleInterval = 15 * time.Minute
	}
	if cfg.RetentionInterval <= 0 {
		cfg.RetentionInterval = time.Hour
	}
	if cfg.Journalctl == "" {
		if p, err := exec.LookPath("journalctl"); err == nil {
			cfg.Journalctl = p
		}
	}
	if len(cfg.WritePaths) == 0 {
		cfg.WritePaths = []string{"/var/log", "/var/lib/docker/containers", "/var/lib/docker/volumes"}
	}
	for i, p := range cfg.WritePaths {
		cfg.WritePaths[i] = filepath.Clean(p)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	c := &Core{cfg: cfg, started: time.Now(), log: cfg.Log, byID: map[string]*api.Source{}}
	if !cfg.DisableDocker {
		d, err := NewDocker(cfg.DockerHost)
		if err != nil {
			c.dockerErr = err.Error()
		} else {
			c.docker = d
		}
	}
	c.hist = loadHistory(filepath.Join(cfg.DataDir, "history.json.gz"))
	ps, err := loadPolicies(filepath.Join(cfg.DataDir, "policies.json"))
	if err != nil {
		return nil, err
	}
	c.policies = ps
	c.sched = loadRunState(filepath.Join(cfg.DataDir, "schedule.json"))
	// policies saved before per-policy schedules existed get the configured default
	list := ps.get()
	for i := range list {
		if list[i].Schedule.Mode == "" {
			_ = normalizeSchedule(&list[i].Schedule, cfg.RetentionInterval)
		}
	}
	ps.list = list
	return c, nil
}

// Run starts background loops (discovery refresh, size sampling, retention). Blocks until ctx done.
func (c *Core) Run(ctx context.Context) {
	if _, err := c.scan(ctx); err != nil {
		c.log.Warn("initial scan", "err", err)
	}
	c.sample()
	st := time.NewTicker(c.cfg.SampleInterval)
	rt := time.NewTicker(30 * time.Second) // checks which policies are due
	defer st.Stop()
	defer rt.Stop()
	for {
		select {
		case <-ctx.Done():
			c.hist.save()
			return
		case <-st.C:
			if _, err := c.scan(ctx); err == nil {
				c.sample()
			}
		case now := <-rt.C:
			c.runDue(ctx, now)
		}
	}
}

func (c *Core) sample() {
	c.mu.RLock()
	m := make(map[string]sampleIn, len(c.sources))
	for _, s := range c.sources {
		m[s.ID] = sampleIn{app: s.App, bytes: s.TotalSize}
	}
	c.mu.RUnlock()
	c.hist.add(time.Now().Unix(), m)
	c.hist.save()
}

// hp maps a host path to the path visible to this process.
func (c *Core) hp(host string) string {
	if c.cfg.HostRoot == "" || c.cfg.HostRoot == "/" {
		return host
	}
	return filepath.Join(c.cfg.HostRoot, host)
}

func (c *Core) Info(ctx context.Context) (api.Info, error) {
	srcs := c.cachedSources(ctx)
	h, _ := os.Hostname()
	if b, err := os.ReadFile(c.hp("/etc/hostname")); err == nil && c.cfg.HostRoot != "" {
		if s := strings.TrimSpace(string(b)); s != "" {
			h = s
		}
	}
	info := api.Info{
		Hostname: h, Version: api.Version, OS: runtime.GOOS, Arch: runtime.GOARCH,
		HostRoot: c.cfg.HostRoot, Timezone: zoneLabel(), AllowWrite: c.cfg.AllowWrite, UptimeSec: int64(time.Since(c.started).Seconds()),
		Journal: c.cfg.Journalctl != "" && len(c.journalDirs()) > 0,
	}
	if c.cfg.AllowWrite {
		info.WritePaths = c.cfg.WritePaths
	}
	c.mu.RLock()
	info.Docker = c.docker != nil && c.dockerErr == ""
	info.DockerError = c.dockerErr
	c.mu.RUnlock()
	for _, s := range srcs {
		info.TotalBytes += s.TotalSize
	}
	info.SourcesCount = len(srcs)
	info.Disks = c.disks()
	return info, nil
}

func (c *Core) disks() []api.Disk {
	var out []api.Disk
	seen := map[uint64]bool{}
	var selfDev uint64
	if c.cfg.HostRoot != "" {
		if fi, err := os.Stat("/"); err == nil {
			selfDev = devOf(fi)
		}
	}
	for _, p := range []string{"/", "/var/log", "/var/lib/docker/containers", "/var/lib/docker/volumes"} {
		fi, err := os.Stat(c.hp(p))
		if err != nil {
			continue
		}
		dev := devOf(fi)
		if seen[dev] || (selfDev != 0 && dev == selfDev) {
			continue // same filesystem, or just the container's own overlay
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(c.hp(p), &st); err != nil {
			continue
		}
		seen[dev] = true
		bs := uint64(st.Bsize)
		total := st.Blocks * bs
		dup := false
		for _, d := range out { // btrfs subvolumes etc.: different st_dev, same filesystem
			if d.Total == total && d.Free == st.Bavail*bs {
				dup = true
			}
		}
		if dup {
			continue
		}
		label := strings.TrimSuffix(strings.TrimSuffix(p, "/containers"), "/volumes")
		out = append(out, api.Disk{Path: label, Total: total, Free: st.Bavail * bs, Used: total - st.Bfree*bs})
	}
	return out
}

func devOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev)
	}
	return 0
}

func (c *Core) Sources(ctx context.Context, refresh bool) ([]api.Source, error) {
	c.mu.RLock()
	fresh := !c.lastScan.IsZero() && time.Since(c.lastScan) < 60*time.Second
	out := c.sources
	c.mu.RUnlock()
	if fresh && !refresh {
		return withGrowth(out, c.hist), nil
	}
	s, err := c.scan(ctx)
	return withGrowth(s, c.hist), err
}

// cachedSources never waits on a rescan once one has completed: stale results are
// returned as-is and refreshed in the background. Info is the hub's liveness probe
// and must answer well within its timeout even when a scan takes seconds.
func (c *Core) cachedSources(ctx context.Context) []api.Source {
	c.mu.RLock()
	out, last := c.sources, c.lastScan
	c.mu.RUnlock()
	if last.IsZero() {
		s, _ := c.Sources(ctx, false)
		return s
	}
	if time.Since(last) >= 60*time.Second && c.refreshing.CompareAndSwap(false, true) {
		go func() {
			defer c.refreshing.Store(false)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if _, err := c.scan(ctx); err != nil {
				c.log.Warn("background scan", "err", err)
			}
		}()
	}
	return withGrowth(out, c.hist)
}

func withGrowth(in []api.Source, h *history) []api.Source {
	out := make([]api.Source, len(in))
	copy(out, in)
	for i := range out {
		out[i].Growth24h = h.growth(out[i].ID, out[i].TotalSize, 24*time.Hour)
	}
	return out
}

func (c *Core) source(id string) (api.Source, ContainerJSON, error) {
	if _, err := c.Sources(context.Background(), false); err != nil && len(c.sources) == 0 {
		return api.Source{}, ContainerJSON{}, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.byID[id]
	if !ok {
		return api.Source{}, ContainerJSON{}, fmt.Errorf("unknown source %q", id)
	}
	return *s, c.containers[id], nil
}

// ---------- discovery ----------

var denyMountRoots = []string{"/", "/proc", "/sys", "/dev", "/etc", "/usr", "/bin", "/sbin", "/lib", "/boot", "/run", "/var/run", "/var/lib/docker", "/home", "/root", "/tmp"}

func (c *Core) scan(ctx context.Context) ([]api.Source, error) {
	c.scanMu.Lock()
	defer c.scanMu.Unlock()

	acc := newAccumulator(c)
	conts := map[string]ContainerJSON{}
	dockerErr := ""
	if c.docker != nil {
		list, err := c.docker.Containers(ctx)
		if err != nil {
			dockerErr = err.Error()
		}
		for _, cj := range list {
			if strings.EqualFold(cj.Config.Labels["strawlog.ignore"], "true") {
				continue
			}
			app := firstNonEmpty(cj.Config.Labels["strawlog.app"], cj.Config.Labels["com.docker.compose.project"], cj.Name)
			id := "docker:" + cj.Name
			conts[id] = cj
			acc.addDocker(id, app, cj)

			self := c.isSelf(cj)
			for _, m := range cj.Mounts {
				if self || (m.Type != "bind" && m.Type != "volume") {
					continue
				}
				// whole log roots mounted into a container (log shippers etc.) don't make it the owner
				if src := filepath.Clean(m.Source); src == "/var/log" || src == "/var/lib/docker/containers" || src == "/var/lib/docker/volumes" {
					continue
				}
				if denied(m.Source) && !strings.HasPrefix(m.Source, "/var/lib/docker/volumes/") && !strings.HasPrefix(m.Source, "/var/log") && !underHome(m.Source) {
					continue
				}
				origin := m.Type
				acc.walk(m.Source, app, origin, 3, false)
			}
			if lp := cj.Config.Labels["strawlog.paths"]; lp != "" {
				for _, p := range strings.Split(lp, ",") {
					p = strings.TrimSpace(p)
					if hostp, ok := mapContainerPath(cj.Mounts, p); ok {
						acc.glob(hostp, app, "label")
					}
				}
			}
		}
	}
	// /var/log: lower priority than container mounts (addFile keeps first owner)
	acc.walkVarLog()
	for _, e := range c.cfg.ExtraPaths {
		app, p, ok := strings.Cut(e, "=")
		if !ok {
			p, app = app, ""
		}
		if app == "" {
			app = filepath.Base(filepath.Dir(p))
		}
		acc.glob(p, app, "extra")
	}
	srcs := acc.finish()
	srcs = append(srcs, c.journalSources()...)
	sort.Slice(srcs, func(i, j int) bool {
		if srcs[i].App != srcs[j].App {
			return srcs[i].App < srcs[j].App
		}
		return srcs[i].Name < srcs[j].Name
	})

	by := make(map[string]*api.Source, len(srcs))
	for i := range srcs {
		by[srcs[i].ID] = &srcs[i]
	}
	c.mu.Lock()
	c.sources, c.byID, c.containers, c.lastScan = srcs, by, conts, time.Now()
	if c.docker != nil {
		c.dockerErr = dockerErr
	}
	c.mu.Unlock()
	if dockerErr != "" {
		return srcs, errors.New(dockerErr)
	}
	return srcs, nil
}

// isSelf detects our own container (its hostname is the container ID prefix), whose
// mounts are the host views we already scan.
func (c *Core) isSelf(cj ContainerJSON) bool {
	h, _ := os.Hostname()
	if len(h) >= 12 && strings.HasPrefix(cj.ID, h) {
		return true
	}
	for _, m := range cj.Mounts {
		if c.cfg.HostRoot != "" && strings.HasPrefix(m.Destination, c.cfg.HostRoot+"/") {
			return true
		}
	}
	return false
}

func underHome(p string) bool {
	return strings.HasPrefix(p, "/home/") || strings.HasPrefix(p, "/root/") || strings.HasPrefix(p, "/srv/") || strings.HasPrefix(p, "/opt/") || strings.HasPrefix(p, "/mnt/") || strings.HasPrefix(p, "/data")
}

func denied(p string) bool {
	p = filepath.Clean(p)
	for _, d := range denyMountRoots {
		if p == d || (d != "/" && strings.HasPrefix(p, d+"/")) {
			return true
		}
	}
	return false
}

func mapContainerPath(mounts []Mount, cpath string) (string, bool) {
	best := -1
	for i, m := range mounts {
		d := strings.TrimRight(m.Destination, "/")
		if cpath == d || strings.HasPrefix(cpath, d+"/") {
			if best < 0 || len(d) > len(mounts[best].Destination) {
				best = i
			}
		}
	}
	if best < 0 {
		return "", false
	}
	m := mounts[best]
	rest := strings.TrimPrefix(cpath, strings.TrimRight(m.Destination, "/"))
	return filepath.Join(m.Source, rest), true
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func (c *Core) writable(hostPath string) bool {
	if !c.cfg.AllowWrite {
		return false
	}
	clean := filepath.Clean(hostPath)
	ok := false
	for _, p := range c.cfg.WritePaths {
		if clean == p || strings.HasPrefix(clean, p+"/") {
			ok = true
			break
		}
	}
	if !ok {
		return false
	}
	return syscall.Access(filepath.Dir(c.hp(clean)), 2) == nil
}
