package core

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"strawlog/internal/api"
)

var (
	compressExt  = regexp.MustCompile(`\.(gz|xz|bz2|zst|zip|lz4)$`)
	rotSuffix    = regexp.MustCompile(`[._-]\d{1,14}$`)
	logLike      = regexp.MustCompile(`(?i)(\.log|\.out|\.err)(\.\d+)?(\.(gz|xz|bz2|zst))?$|\.log[._-]\d{4,14}(\.(gz|xz|bz2|zst))?$|[_-]log(\.json|\.txt)?$`)
	logDirName   = regexp.MustCompile(`(?i)^(logs?|.*[_-]logs?)$`)
	notText      = regexp.MustCompile(`(?i)\.(crt|key|pem|db|sqlite\d?|so|bin|png|jpe?g|gif|zip|tar|pid|lock|sock|journal~?)$`)
	binaryVarLog = map[string]bool{"wtmp": true, "btmp": true, "lastlog": true, "faillog": true, "utmp": true, "tallylog": true}
	skipDirs     = map[string]bool{"node_modules": true, ".git": true, "journal": true, "__pycache__": true, ".cache": true}
)

const maxVisitPerRoot = 20000

type fileEntry struct {
	host  string
	size  int64
	mtime int64
	app   string
	orig  string
}

type accumulator struct {
	c      *Core
	files  map[string]fileEntry // by host path
	docker []api.Source
}

func newAccumulator(c *Core) *accumulator {
	return &accumulator{c: c, files: map[string]fileEntry{}}
}

func (a *accumulator) addFile(host string, fi fs.FileInfo, app, origin string) {
	if _, ok := a.files[host]; ok {
		return // first owner wins (container mounts are scanned before /var/log)
	}
	a.files[host] = fileEntry{host: host, size: fi.Size(), mtime: fi.ModTime().Unix(), app: app, orig: origin}
}

// walk scans hostDir (a host path) for log-like files.
func (a *accumulator) walk(hostDir, app, origin string, maxDepth int, anyFile bool) {
	local := a.c.hp(hostDir)
	st, err := os.Lstat(local)
	if err != nil {
		return
	}
	if st.Mode().IsRegular() {
		if logLike.MatchString(st.Name()) || anyFile {
			a.addFile(hostDir, st, app, origin)
		}
		return
	}
	if !st.IsDir() {
		return
	}
	baseDepth := strings.Count(filepath.Clean(local), string(os.PathSeparator))
	visited := 0
	_ = filepath.WalkDir(local, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		visited++
		if visited > maxVisitPerRoot {
			return fs.SkipAll
		}
		if d.IsDir() {
			if p != local && (skipDirs[d.Name()] || strings.Count(p, string(os.PathSeparator))-baseDepth >= maxDepth) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		if notText.MatchString(name) {
			return nil
		}
		if !(anyFile || logLike.MatchString(name) || (inLogDir(local, p) && !strings.HasPrefix(name, "."))) {
			return nil
		}
		if anyFile && binaryVarLog[compressExt.ReplaceAllString(rotSuffix.ReplaceAllString(name, ""), "")] {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		a.addFile(a.toHost(p), fi, app, origin)
		return nil
	})
}

// inLogDir reports whether a path segment below root is a log directory (log, logs, app-logs…).
func inLogDir(root, p string) bool {
	rel, err := filepath.Rel(root, filepath.Dir(p))
	if err != nil {
		return false
	}
	for _, seg := range strings.Split(rel, string(os.PathSeparator)) {
		if logDirName.MatchString(seg) {
			return true
		}
	}
	// the mount itself may be the log dir (e.g. /srv/app/logs mounted at /app/logs)
	return logDirName.MatchString(filepath.Base(root))
}

func (a *accumulator) toHost(local string) string {
	r := a.c.cfg.HostRoot
	if r == "" || r == "/" {
		return local
	}
	return "/" + strings.TrimPrefix(strings.TrimPrefix(local, r), "/")
}

func (a *accumulator) walkVarLog() {
	local := a.c.hp("/var/log")
	entries, err := os.ReadDir(local)
	if err != nil {
		return
	}
	for _, e := range entries {
		host := "/var/log/" + e.Name()
		if e.IsDir() {
			if e.Name() == "journal" || e.Name() == "private" {
				continue
			}
			a.walk(host, e.Name(), "var-log", 4, true)
			continue
		}
		if e.Type()&fs.ModeSymlink != 0 || !e.Type().IsRegular() {
			continue
		}
		base := compressExt.ReplaceAllString(e.Name(), "")
		for rotSuffix.MatchString(base) {
			base = rotSuffix.ReplaceAllString(base, "")
		}
		if binaryVarLog[base] {
			continue
		}
		if fi, err := e.Info(); err == nil {
			a.addFile(host, fi, "system", "var-log")
		}
	}
}

func (a *accumulator) glob(hostPattern, app, origin string) {
	matches, err := filepath.Glob(a.c.hp(hostPattern))
	if err != nil {
		return
	}
	for _, m := range matches {
		a.walk(a.toHost(m), app, origin, 3, true)
	}
}

func (a *accumulator) addDocker(id, app string, cj ContainerJSON) {
	s := api.Source{
		ID: id, Kind: api.KindDocker, App: app, Name: cj.Name, Container: cj.ID[:min(12, len(cj.ID))],
		State: cj.State.Status, LogDriver: cj.HostConfig.LogConfig.Type, LogOpts: cj.HostConfig.LogConfig.Config,
		Path: cj.LogPath, Origin: "docker",
	}
	if s.Path == "" && s.LogDriver == "local" {
		s.Path = "/var/lib/docker/containers/" + cj.ID + "/local-logs/container.log"
	}
	s.Readable = true // via API
	if s.Path != "" {
		if fi, err := os.Stat(a.c.hp(s.Path)); err == nil {
			s.Size = fi.Size()
			s.ModTime = fi.ModTime().Unix()
			s.TotalSize = s.Size
			s.Writable = a.c.writable(s.Path)
		}
		dir := filepath.Dir(a.c.hp(s.Path))
		base := filepath.Base(s.Path)
		if ents, err := os.ReadDir(dir); err == nil {
			for _, e := range ents {
				n := e.Name()
				if n != base && strings.HasPrefix(n, base+".") {
					if fi, err := e.Info(); err == nil {
						s.Rotated = append(s.Rotated, api.FileInfo{Path: filepath.Join(filepath.Dir(s.Path), n), Size: fi.Size(), ModTime: fi.ModTime().Unix(), Compressed: compressExt.MatchString(n)})
						s.TotalSize += fi.Size()
					}
				}
			}
		}
	}
	a.docker = append(a.docker, s)
}

// baseOf returns the active log name for a (possibly rotated) file name.
func baseOf(name string) string {
	b := compressExt.ReplaceAllString(name, "")
	for i := 0; i < 2 && rotSuffix.MatchString(b); i++ {
		nb := rotSuffix.ReplaceAllString(b, "")
		if nb == "" || strings.HasSuffix(nb, ".") {
			break
		}
		b = nb
	}
	return b
}

func (a *accumulator) finish() []api.Source {
	groups := map[string]*api.Source{}
	for host, f := range a.files {
		dir, name := filepath.Split(host)
		base := baseOf(name)
		active := filepath.Join(dir, base)
		if _, ok := a.files[active]; !ok && base != name {
			// keep e.g. "app-2026-11-01.log" alone when "app" doesn't exist
			if !strings.Contains(base, ".") {
				active = host
			}
		}
		g, ok := groups[active]
		if !ok {
			owner := f
			if af, ok := a.files[active]; ok {
				owner = af
			}
			g = &api.Source{ID: "file:" + active, Kind: api.KindFile, App: owner.app, Name: strings.TrimPrefix(active, "/var/log/"), Path: active, Origin: owner.orig}
			if owner.orig != "var-log" {
				g.Name = filepath.Base(active)
			}
			groups[active] = g
		}
		if host == active {
			g.Size, g.ModTime = f.size, f.mtime
		} else {
			g.Rotated = append(g.Rotated, api.FileInfo{Path: host, Size: f.size, ModTime: f.mtime, Compressed: compressExt.MatchString(host)})
			if f.mtime > g.ModTime && g.Size == 0 {
				g.ModTime = f.mtime
			}
		}
		g.TotalSize += f.size
	}
	out := a.docker
	for _, g := range groups {
		sort.Slice(g.Rotated, func(i, j int) bool { return g.Rotated[i].ModTime > g.Rotated[j].ModTime })
		if f, err := os.Open(a.c.hp(g.Path)); err == nil {
			g.Readable = true
			f.Close()
		} else if len(g.Rotated) > 0 {
			g.Readable = true
		}
		g.Writable = a.c.writable(g.Path)
		out = append(out, *g)
	}
	return out
}
