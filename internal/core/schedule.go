package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"strawlog/internal/api"
)

const (
	ModeInterval = "interval"
	ModeDaily    = "daily"
	minEveryMin  = 5
)

// normalizeSchedule fills defaults (older policies have no schedule) and validates it.
func normalizeSchedule(s *api.Schedule, defEvery time.Duration) error {
	if s.Mode == "" {
		s.Mode = ModeInterval
	}
	switch s.Mode {
	case ModeInterval:
		if s.EveryMin == 0 {
			s.EveryMin = max(minEveryMin, int(defEvery/time.Minute))
		}
		if s.EveryMin < minEveryMin {
			return fmt.Errorf("schedule: run at most every %d minutes", minEveryMin)
		}
		s.At = ""
	case ModeDaily:
		if _, err := time.Parse("15:04", s.At); err != nil {
			return fmt.Errorf("schedule: time must be HH:MM (got %q)", s.At)
		}
		s.EveryMin = 0
	default:
		return fmt.Errorf("schedule: unknown mode %q", s.Mode)
	}
	return nil
}

// nextRun is the first scheduled time strictly after last.
func nextRun(s api.Schedule, last time.Time) time.Time {
	if s.Mode == ModeDaily {
		t, _ := time.Parse("15:04", s.At)
		l := last.In(time.Local)
		n := time.Date(l.Year(), l.Month(), l.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
		if !n.After(l) {
			n = time.Date(l.Year(), l.Month(), l.Day()+1, t.Hour(), t.Minute(), 0, 0, time.Local)
		}
		return n
	}
	return last.Add(time.Duration(s.EveryMin) * time.Minute)
}

// runState remembers, per policy, the anchor the next run is computed from and when it last ran
// on schedule. It is persisted so restarts don't reset the clock.
type runState struct {
	path string
	mu   sync.Mutex
	m    map[string]runEntry
}

type runEntry struct {
	Anchor  int64 `json:"anchor"`            // last run, or when the schedule was set/changed
	LastRun int64 `json:"lastRun,omitempty"` // last scheduled run
}

func loadRunState(path string) *runState {
	rs := &runState{path: path, m: map[string]runEntry{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &rs.m)
	}
	return rs
}

func (rs *runState) saveLocked() {
	if b, err := json.Marshal(rs.m); err == nil {
		_ = writeFileAtomic(rs.path, b, 0o600)
	}
}

// entry returns the state for a policy, anchoring it at now if it has none yet.
func (rs *runState) entry(id string, now time.Time) runEntry {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	e, ok := rs.m[id]
	if !ok {
		e = runEntry{Anchor: now.Unix()}
		rs.m[id] = e
		rs.saveLocked()
	}
	return e
}

func (rs *runState) mark(ids []string, at time.Time) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, id := range ids {
		rs.m[id] = runEntry{Anchor: at.Unix(), LastRun: at.Unix()}
	}
	rs.saveLocked()
}

// reanchor restarts the clock for policies that are new, changed schedule or were just enabled,
// and forgets deleted ones.
func (rs *runState) reanchor(old, cur []api.Policy, now time.Time) {
	prev := map[string]api.Policy{}
	for _, p := range old {
		prev[p.ID] = p
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	keep := map[string]bool{}
	for _, p := range cur {
		keep[p.ID] = true
		o, ok := prev[p.ID]
		if !ok || o.Schedule != p.Schedule || (p.Enabled && !o.Enabled) {
			e := rs.m[p.ID]
			e.Anchor = now.Unix()
			rs.m[p.ID] = e
		}
	}
	for id := range rs.m {
		if !keep[id] {
			delete(rs.m, id)
		}
	}
	rs.saveLocked()
}

// withTimes fills the read-only LastRun/NextRun fields.
func (c *Core) withTimes(list []api.Policy) []api.Policy {
	now := time.Now()
	for i := range list {
		p := &list[i]
		e := c.sched.entry(p.ID, now)
		p.LastRun = e.LastRun
		p.NextRun = 0
		if p.Enabled && c.cfg.AllowWrite {
			p.NextRun = nextRun(p.Schedule, time.Unix(e.Anchor, 0)).Unix()
		}
	}
	return list
}

// runDue applies every enabled policy whose next run time has passed, in one batch.
func (c *Core) runDue(ctx context.Context, now time.Time) {
	if !c.cfg.AllowWrite {
		return
	}
	var due []string
	for _, p := range c.policies.get() {
		if p.Enabled && !nextRun(p.Schedule, time.Unix(c.sched.entry(p.ID, now).Anchor, 0)).After(now) {
			due = append(due, p.ID)
		}
	}
	if len(due) == 0 {
		return
	}
	c.sched.mark(due, now)
	p, err := c.apply(ctx, api.PlanReq{PolicyIDs: due}, "schedule")
	if err != nil {
		c.log.Warn("scheduled retention", "err", err)
	} else if len(p.Actions) > 0 {
		c.log.Info("scheduled retention", "policies", len(due), "actions", len(p.Actions), "freed", p.FreedBytes)
	}
}

func zoneLabel() string {
	name, off := time.Now().Zone()
	if off == 0 {
		return "UTC"
	}
	loc := time.Local.String()
	if loc == "Local" || loc == "" {
		loc = name
	}
	return fmt.Sprintf("%s (%s)", loc, time.Now().Format("-07:00"))
}
