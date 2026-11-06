package core

import (
	"context"
	"fmt"
	"sort"
	"time"

	"strawlog/internal/api"
)

const (
	gb = 1 << 30
)

func human(b int64) string {
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/gb)
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/mb)
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}

const dockerLogFix = `# per service in docker-compose.yml
logging:
  driver: json-file        # or "local" (compressed, rotated by default)
  options:
    max-size: "10m"
    max-file: "3"

# or globally in /etc/docker/daemon.json (applies to NEW containers), then restart docker:
{ "log-driver": "json-file", "log-opts": { "max-size": "10m", "max-file": "3" } }`

func (c *Core) Health(ctx context.Context) ([]api.Finding, error) {
	srcs, err := c.Sources(ctx, false)
	if err != nil && len(srcs) == 0 {
		return nil, err
	}
	var out []api.Finding
	add := func(sev, code string, s *api.Source, title, detail, fix string) {
		f := api.Finding{Severity: sev, Code: code, Title: title, Detail: detail, Fix: fix, Key: code}
		if s != nil {
			f.SourceID, f.App = s.ID, s.App
			f.Key = code + "|" + s.ID
		}
		out = append(out, f)
	}

	for _, d := range c.disks() {
		if d.Total == 0 {
			continue
		}
		pct := float64(d.Used) / float64(d.Total) * 100
		n := len(out)
		if pct >= 90 {
			add("critical", "disk-full", nil, fmt.Sprintf("Filesystem %s is %.0f%% full", d.Path, pct), fmt.Sprintf("%s free of %s.", human(int64(d.Free)), human(int64(d.Total))), "Look at the biggest sources on the Sources page and add a retention policy.")
		} else if pct >= 80 {
			add("warning", "disk-high", nil, fmt.Sprintf("Filesystem %s is %.0f%% full", d.Path, pct), fmt.Sprintf("%s free of %s.", human(int64(d.Free)), human(int64(d.Total))), "")
		}
		if len(out) > n {
			out[n].Key += "|" + d.Path
		}
	}

	if c.docker == nil || c.dockerErr != "" {
		msg := c.dockerErr
		if msg == "" {
			msg = "docker integration disabled"
		}
		add("info", "docker-unavailable", nil, "Docker is not reachable", msg, "Mount /var/run/docker.sock (ideally through the bundled socket proxy) and set DOCKER_HOST.")
	}

	for i := range srcs {
		s := &srcs[i]
		switch s.Kind {
		case api.KindDocker:
			unbounded := s.LogDriver == "json-file" && s.LogOpts["max-size"] == ""
			if unbounded {
				sev := "warning"
				if s.TotalSize > gb {
					sev = "critical"
				}
				add(sev, "docker-unbounded", s, fmt.Sprintf("Container %s has no log size limit", s.Name),
					fmt.Sprintf("json-file driver without max-size; log is %s and will grow forever.", human(s.TotalSize)), dockerLogFix)
			}
			if s.LogDriver == "json-file" && s.Size == 0 && s.ModTime == 0 {
				add("info", "docker-size-unknown", s, fmt.Sprintf("Can't measure log size of %s", s.Name), "The container log file isn't visible to Straw Log.", "Mount /var/lib/docker/containers (read-only is enough) into the Straw Log container.")
			}
			if s.LogDriver == "none" {
				add("info", "docker-nolog", s, fmt.Sprintf("Container %s has logging disabled", s.Name), "log driver is 'none'.", "")
			}
		case api.KindFile:
			if s.Size > 200*mb && len(s.Rotated) == 0 {
				add("warning", "not-rotated", s, fmt.Sprintf("%s is %s and never rotated", s.Name, human(s.Size)),
					"No rotated siblings were found next to this file; nothing seems to be rotating it.",
					fmt.Sprintf("Add a logrotate rule, e.g. /etc/logrotate.d/%s:\n%s {\n  daily\n  rotate 7\n  maxsize 100M\n  compress\n  delaycompress\n  missingok\n  notifempty\n  copytruncate\n}\n\nor a Straw Log retention policy with \"Max active size\".", s.App, s.Path))
			}
			var raw int64
			for _, r := range s.Rotated {
				if !r.Compressed {
					raw += r.Size
				}
			}
			if raw > 500*mb {
				add("info", "uncompressed-rotations", s, fmt.Sprintf("%s keeps %s of uncompressed rotated logs", s.Name, human(raw)), "", "Enable 'compress' in logrotate or a policy with \"Compress rotated\".")
			}
		case api.KindJournal:
			if s.TotalSize > 4*gb {
				add("warning", "journal-big", s, fmt.Sprintf("journald uses %s", human(s.TotalSize)), "", "Set SystemMaxUse=1G in /etc/systemd/journald.conf (then systemctl restart systemd-journald), or add a policy with \"Max total size\" for the journal.")
			}
		}
		if s.TotalSize > 5*gb {
			add("critical", "huge-source", s, fmt.Sprintf("%s (%s) uses %s", s.Name, s.App, human(s.TotalSize)), "", "")
		} else if s.TotalSize > gb {
			add("warning", "big-source", s, fmt.Sprintf("%s (%s) uses %s", s.Name, s.App, human(s.TotalSize)), "", "")
		}
		if g := c.hist.growth(s.ID, s.TotalSize, 24*time.Hour); g > gb {
			add("warning", "fast-growth", s, fmt.Sprintf("%s grows %s/day", s.Name, human(g)), "At this rate it will dominate the disk; check the log level of this app.", "")
		}
	}
	out = append(out, c.noisyFindings(ctx, srcs)...)
	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Severity] < rank[out[j].Severity] })
	return out, nil
}

// noisyFindings samples the tail of the biggest growing sources to spot DEBUG logging in production.
func (c *Core) noisyFindings(ctx context.Context, srcs []api.Source) []api.Finding {
	type cand struct {
		s api.Source
		g int64
	}
	var cs []cand
	for _, s := range srcs {
		if s.Kind == api.KindJournal || !s.Readable {
			continue
		}
		g := c.hist.growth(s.ID, s.TotalSize, 24*time.Hour)
		if g > 50*mb || s.Size > 100*mb {
			cs = append(cs, cand{s, g})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].g > cs[j].g })
	if len(cs) > 8 {
		cs = cs[:8]
	}
	var out []api.Finding
	for _, cd := range cs {
		tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		lines, err := c.Tail(tctx, api.TailReq{Source: cd.s.ID, Lines: 500})
		cancel()
		if err != nil || len(lines) < 50 {
			continue
		}
		dbg := 0
		for _, l := range lines {
			if l.Level == "debug" {
				dbg++
			}
		}
		if dbg*100/len(lines) >= 40 {
			out = append(out, api.Finding{Key: "debug-logging|" + cd.s.ID, Severity: "warning", Code: "debug-logging", SourceID: cd.s.ID, App: cd.s.App,
				Title:  fmt.Sprintf("%s logs mostly DEBUG/TRACE (%d%% of recent lines)", cd.s.Name, dbg*100/len(lines)),
				Detail: "Verbose logging in production costs disk and I/O and can slow the app down.",
				Fix:    "Lower the log level (e.g. LOG_LEVEL=info / RUST_LOG=info / ASPNETCORE_LOGGING__LOGLEVEL__DEFAULT=Information)."})
		}
	}
	return out
}
