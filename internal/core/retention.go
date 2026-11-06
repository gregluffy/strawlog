package core

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"strawlog/internal/api"
)

const mb = 1 << 20

// ---------- policy store ----------

type policyStore struct {
	path string
	mu   sync.RWMutex
	list []api.Policy
}

func loadPolicies(path string) (*policyStore, error) {
	ps := &policyStore{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ps, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &ps.list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ps, nil
}

func (ps *policyStore) get() []api.Policy {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return slices.Clone(ps.list)
}

func (ps *policyStore) set(list []api.Policy) error {
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if err := writeFileAtomic(ps.path, b, 0o600); err != nil {
		return err
	}
	ps.list = list
	return nil
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func validatePolicy(p *api.Policy) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return errors.New("policy name is required")
	}
	if p.ID == "" {
		p.ID = randID()
	}
	if p.MaxAgeDays < 0 || p.MaxRotated < 0 || p.MaxActiveMB < 0 || p.MaxTotalMB < 0 {
		return fmt.Errorf("policy %q: limits must be >= 0", p.Name)
	}
	if p.Match.PathGlob != "" {
		if _, err := filepath.Match(p.Match.PathGlob, "/x"); err != nil {
			return fmt.Errorf("policy %q: bad glob: %v", p.Name, err)
		}
	}
	if p.MaxAgeDays == 0 && p.MaxRotated == 0 && !p.CompressRotated && p.MaxActiveMB == 0 && p.MaxTotalMB == 0 {
		return fmt.Errorf("policy %q does nothing: set at least one limit", p.Name)
	}
	return nil
}

func (c *Core) Policies(ctx context.Context) ([]api.Policy, error) { return c.policies.get(), nil }

func (c *Core) SetPolicies(ctx context.Context, list []api.Policy) error {
	seen := map[string]bool{}
	for i := range list {
		if err := validatePolicy(&list[i]); err != nil {
			return err
		}
		if seen[list[i].ID] {
			list[i].ID = randID()
		}
		seen[list[i].ID] = true
	}
	return c.policies.set(list)
}

func (c *Core) Runs(ctx context.Context) ([]api.Plan, error) {
	c.runsMu.Lock()
	defer c.runsMu.Unlock()
	out := slices.Clone(c.runs)
	slices.Reverse(out)
	return out, nil
}

// ---------- planning ----------

func matches(p api.Policy, s api.Source) bool {
	m := p.Match
	if len(m.Apps) > 0 && !slices.Contains(m.Apps, s.App) {
		return false
	}
	if len(m.Kinds) > 0 && !slices.Contains(m.Kinds, s.Kind) {
		return false
	}
	if len(m.Sources) > 0 && !slices.Contains(m.Sources, s.ID) {
		return false
	}
	if m.PathGlob != "" {
		ok, _ := filepath.Match(m.PathGlob, s.Path)
		if !ok {
			return false
		}
	}
	return true
}

func (c *Core) selectPolicies(r api.PlanReq) []api.Policy {
	if r.Policy != nil {
		p := *r.Policy
		if p.Name == "" {
			p.Name = "preview"
		}
		return []api.Policy{p}
	}
	var out []api.Policy
	for _, p := range c.policies.get() {
		if len(r.PolicyIDs) > 0 {
			if slices.Contains(r.PolicyIDs, p.ID) {
				out = append(out, p)
			}
		} else if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

func (c *Core) buildPlan(ctx context.Context, r api.PlanReq) (api.Plan, []api.Policy, error) {
	srcs, err := c.Sources(ctx, true)
	if err != nil && len(srcs) == 0 {
		return api.Plan{}, nil, err
	}
	var owners []api.Policy
	plan := api.Plan{DryRun: true, At: time.Now().Unix(), Actions: []api.Action{}}
	claimed := map[string]bool{} // path+op already planned by an earlier policy
	for _, p := range c.selectPolicies(r) {
		for _, s := range srcs {
			if !matches(p, s) {
				continue
			}
			for _, a := range c.planSource(p, s) {
				key := a.Path + "|" + a.Op
				if claimed[a.Path+"|delete"] || claimed[key] {
					continue
				}
				claimed[key] = true
				if a.Error == "" && !c.writable(a.Path) {
					a.Error = c.notWritableReason(a.Path)
				}
				plan.Actions = append(plan.Actions, a)
				owners = append(owners, p)
				if a.Error == "" {
					plan.FreedBytes += a.Bytes
				}
			}
		}
	}
	return plan, owners, nil
}

func (c *Core) notWritableReason(path string) string {
	if !c.cfg.AllowWrite {
		return "read-only node (set STRAWLOG_ALLOW_WRITE=true on this node)"
	}
	clean := filepath.Clean(path)
	for _, p := range c.cfg.WritePaths {
		if clean == p || strings.HasPrefix(clean, p+"/") {
			return "directory is not writable (mount it :rw)"
		}
	}
	return "path outside STRAWLOG_WRITE_PATHS"
}

func (c *Core) planSource(p api.Policy, s api.Source) []api.Action {
	var acts []api.Action
	add := func(path, op string, bytes int64, reason string) {
		acts = append(acts, api.Action{Policy: p.Name, SourceID: s.ID, Path: path, Op: op, Bytes: bytes, Reason: reason})
	}
	if s.Kind == api.KindJournal {
		if p.MaxTotalMB > 0 || p.MaxAgeDays > 0 {
			var free int64
			var why []string
			if p.MaxTotalMB > 0 {
				free = max(0, s.TotalSize-p.MaxTotalMB*mb)
				why = append(why, fmt.Sprintf("size > %d MB", p.MaxTotalMB))
			}
			if p.MaxAgeDays > 0 {
				why = append(why, fmt.Sprintf("entries older than %dd", p.MaxAgeDays))
			}
			if free > 0 || p.MaxAgeDays > 0 {
				add(s.Path, "vacuum", free, "journald vacuum: "+strings.Join(why, ", ")+" (archived files only)")
			}
		}
		return acts
	}
	now := time.Now()
	deleted := map[string]bool{}
	remaining := s.Size
	for i, r := range s.Rotated { // newest first
		switch {
		case p.MaxRotated > 0 && i >= p.MaxRotated:
			add(r.Path, "delete", r.Size, fmt.Sprintf("keeps only %d rotated files", p.MaxRotated))
			deleted[r.Path] = true
		case p.MaxAgeDays > 0 && now.Sub(time.Unix(r.ModTime, 0)) > time.Duration(p.MaxAgeDays)*24*time.Hour:
			add(r.Path, "delete", r.Size, fmt.Sprintf("older than %d days", p.MaxAgeDays))
			deleted[r.Path] = true
		default:
			remaining += r.Size
		}
	}
	if p.CompressRotated && s.Kind == api.KindFile {
		for _, r := range s.Rotated {
			if !deleted[r.Path] && !r.Compressed && now.Sub(time.Unix(r.ModTime, 0)) > time.Hour && r.Size > 0 {
				save := r.Size * 85 / 100
				add(r.Path, "compress", save, "gzip rotated file (estimate ~85% saved)")
				remaining -= save
			}
		}
	}
	activeOver := p.MaxActiveMB > 0 && s.Size > p.MaxActiveMB*mb
	if activeOver {
		if p.ArchiveBeforeTruncate {
			add(s.Path, "archive-truncate", s.Size*85/100, fmt.Sprintf("active file > %d MB; gzip copy kept, then truncated", p.MaxActiveMB))
			remaining -= s.Size * 85 / 100
		} else {
			add(s.Path, "truncate", s.Size, fmt.Sprintf("active file > %d MB", p.MaxActiveMB))
			remaining -= s.Size
		}
	}
	if p.MaxTotalMB > 0 && remaining > p.MaxTotalMB*mb {
		// delete oldest remaining rotated files first
		for i := len(s.Rotated) - 1; i >= 0 && remaining > p.MaxTotalMB*mb; i-- {
			r := s.Rotated[i]
			if deleted[r.Path] {
				continue
			}
			add(r.Path, "delete", r.Size, fmt.Sprintf("total > %d MB (oldest first)", p.MaxTotalMB))
			deleted[r.Path] = true
			remaining -= r.Size
		}
		if remaining > p.MaxTotalMB*mb && !activeOver && s.Size > 0 {
			add(s.Path, "truncate", s.Size, fmt.Sprintf("total still > %d MB after removing rotated files", p.MaxTotalMB))
		}
	}
	return acts
}

func (c *Core) Plan(ctx context.Context, r api.PlanReq) (api.Plan, error) {
	p, _, err := c.buildPlan(ctx, r)
	return p, err
}

func (c *Core) Apply(ctx context.Context, r api.PlanReq) (api.Plan, error) {
	if !c.cfg.AllowWrite {
		return api.Plan{}, errors.New("this node is read-only (STRAWLOG_ALLOW_WRITE is not enabled)")
	}
	return c.apply(ctx, r, "manual")
}

func (c *Core) apply(ctx context.Context, r api.PlanReq, trigger string) (api.Plan, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	plan, owners, err := c.buildPlan(ctx, r)
	if err != nil {
		return plan, err
	}
	plan.DryRun = false
	plan.Trigger = trigger
	plan.FreedBytes = 0
	srcByID := map[string]api.Source{}
	srcs, _ := c.Sources(ctx, false)
	for _, s := range srcs {
		srcByID[s.ID] = s
	}
	for i := range plan.Actions {
		a := &plan.Actions[i]
		if a.Error != "" {
			continue
		}
		freed, err := c.execute(ctx, *a, srcByID[a.SourceID], owners[i])
		if err != nil {
			a.Error = err.Error()
			c.log.Warn("retention action failed", "op", a.Op, "path", a.Path, "err", err)
			continue
		}
		a.Done, a.Bytes = true, freed
		plan.FreedBytes += freed
		c.log.Info("retention", "op", a.Op, "path", a.Path, "freed", freed, "policy", a.Policy)
	}
	if len(plan.Actions) > 0 {
		c.runsMu.Lock()
		c.runs = append(c.runs, plan)
		if len(c.runs) > 50 {
			c.runs = c.runs[len(c.runs)-50:]
		}
		c.runsMu.Unlock()
		_, _ = c.scan(ctx)
	}
	return plan, nil
}

func regularFile(local string) (os.FileInfo, error) {
	fi, err := os.Lstat(local)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing: not a regular file")
	}
	return fi, nil
}

func (c *Core) execute(ctx context.Context, a api.Action, s api.Source, p api.Policy) (int64, error) {
	local := c.hp(a.Path)
	switch a.Op {
	case "vacuum":
		before, _ := dirSize(local)
		if err := c.journalVacuum(ctx, s, p.MaxTotalMB, p.MaxAgeDays); err != nil {
			return 0, err
		}
		after, _ := dirSize(local)
		return max(0, before-after), nil
	case "delete":
		fi, err := regularFile(local)
		if err != nil {
			return 0, err
		}
		if a.Path == s.Path {
			return 0, errors.New("refusing to delete an active log file")
		}
		return fi.Size(), os.Remove(local)
	case "compress":
		fi, err := regularFile(local)
		if err != nil {
			return 0, err
		}
		dst := local + ".gz"
		if _, err := os.Lstat(dst); err == nil {
			return 0, fmt.Errorf("%s already exists", dst)
		}
		n, err := gzipTo(local, dst, fi)
		if err != nil {
			return 0, err
		}
		if err := os.Remove(local); err != nil {
			return 0, err
		}
		return fi.Size() - n, nil
	case "truncate", "archive-truncate":
		fi, err := regularFile(local)
		if err != nil {
			return 0, err
		}
		var kept int64
		if a.Op == "archive-truncate" {
			dst := fmt.Sprintf("%s-%s.gz", local, time.Now().Format("20060102150405"))
			if kept, err = gzipTo(local, dst, fi); err != nil {
				return 0, err
			}
		}
		// open without O_TRUNC + Truncate keeps the inode, so writers with O_APPEND keep working
		f, err := os.OpenFile(local, os.O_WRONLY, 0)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		if err := f.Truncate(0); err != nil {
			return 0, err
		}
		return fi.Size() - kept, nil
	}
	return 0, fmt.Errorf("unknown op %q", a.Op)
}

func gzipTo(src, dst string, fi os.FileInfo) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	tmp := dst + ".strawlog-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fi.Mode().Perm())
	if err != nil {
		return 0, err
	}
	gz, _ := gzip.NewWriterLevel(out, gzip.BestSpeed+1)
	_, err = io.Copy(gz, in)
	if cerr := gz.Close(); err == nil {
		err = cerr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	_ = os.Chtimes(tmp, fi.ModTime(), fi.ModTime())
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(tmp, int(st.Uid), int(st.Gid))
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	nfi, err := os.Stat(dst)
	if err != nil {
		return 0, nil
	}
	return nfi.Size(), nil
}
