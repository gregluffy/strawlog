package core

import (
	"path/filepath"
	"testing"
	"time"

	"strawlog/internal/api"
)

func TestNextRun(t *testing.T) {
	base := time.Date(2026, 3, 10, 14, 20, 0, 0, time.Local)
	cases := []struct {
		s    api.Schedule
		want time.Time
	}{
		{api.Schedule{Mode: ModeInterval, EveryMin: 30}, base.Add(30 * time.Minute)},
		{api.Schedule{Mode: ModeDaily, At: "03:00"}, time.Date(2026, 3, 11, 3, 0, 0, 0, time.Local)},
		{api.Schedule{Mode: ModeDaily, At: "18:45"}, time.Date(2026, 3, 10, 18, 45, 0, 0, time.Local)},
		{api.Schedule{Mode: ModeDaily, At: "14:20"}, time.Date(2026, 3, 11, 14, 20, 0, 0, time.Local)},
		// 2026-03-10 is a Tuesday
		{api.Schedule{Mode: ModeWeekly, Weekday: 0, At: "04:00"}, time.Date(2026, 3, 15, 4, 0, 0, 0, time.Local)},
		{api.Schedule{Mode: ModeWeekly, Weekday: 2, At: "18:00"}, time.Date(2026, 3, 10, 18, 0, 0, 0, time.Local)},
		{api.Schedule{Mode: ModeWeekly, Weekday: 2, At: "09:00"}, time.Date(2026, 3, 17, 9, 0, 0, 0, time.Local)},
		{api.Schedule{Mode: ModeMonthly, Day: 1, At: "02:00"}, time.Date(2026, 4, 1, 2, 0, 0, 0, time.Local)},
		{api.Schedule{Mode: ModeMonthly, Day: 15, At: "02:00"}, time.Date(2026, 3, 15, 2, 0, 0, 0, time.Local)},
	}
	for _, c := range cases {
		if got := nextRun(c.s, base); !got.Equal(c.want) {
			t.Errorf("%+v: got %v want %v", c.s, got, c.want)
		}
	}
}

func TestMonthlyClampsToMonthEnd(t *testing.T) {
	s := api.Schedule{Mode: ModeMonthly, Day: 31, At: "03:00"}
	got := nextRun(s, time.Date(2026, 1, 31, 4, 0, 0, 0, time.Local))
	if want := time.Date(2026, 2, 28, 3, 0, 0, 0, time.Local); !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	got = nextRun(s, got)
	if want := time.Date(2026, 3, 31, 3, 0, 0, 0, time.Local); !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestNormalizeSchedule(t *testing.T) {
	s := api.Schedule{}
	if err := normalizeSchedule(&s, time.Hour); err != nil || s.Mode != ModeInterval || s.EveryMin != 60 {
		t.Fatalf("default: %+v %v", s, err)
	}
	for _, bad := range []api.Schedule{{Mode: ModeInterval, EveryMin: 1}, {Mode: ModeDaily, At: "25:00"}, {Mode: ModeWeekly, At: "03:00", Weekday: 7}, {Mode: ModeMonthly, At: "03:00"}, {Mode: "yearly", At: "03:00"}} {
		if err := normalizeSchedule(&bad, time.Hour); err == nil {
			t.Errorf("%+v: expected error", bad)
		}
	}
}

func TestRunDue(t *testing.T) {
	root := t.TempDir()
	c := newTestCore(t, root, true)
	pol := []api.Policy{
		{Name: "fast", Enabled: true, MaxRotated: 1, Schedule: api.Schedule{Mode: ModeInterval, EveryMin: 10}},
		{Name: "off", Enabled: false, MaxRotated: 1, Schedule: api.Schedule{Mode: ModeInterval, EveryMin: 10}},
	}
	if err := c.SetPolicies(t.Context(), pol); err != nil {
		t.Fatal(err)
	}
	list, _ := c.Policies(t.Context())
	if list[0].NextRun == 0 || list[1].NextRun != 0 {
		t.Fatalf("next run: %+v", list)
	}
	now := time.Now()
	c.runDue(t.Context(), now.Add(5*time.Minute))
	if e := c.sched.entry(list[0].ID, now); e.LastRun != 0 {
		t.Fatal("ran too early")
	}
	c.runDue(t.Context(), now.Add(11*time.Minute))
	if e := c.sched.entry(list[0].ID, now); e.LastRun == 0 {
		t.Fatal("enabled policy did not run")
	}
	if e := c.sched.entry(list[1].ID, now); e.LastRun != 0 {
		t.Fatal("disabled policy ran")
	}
	// state survives a restart
	rs := loadRunState(filepath.Join(c.cfg.DataDir, "schedule.json"))
	if rs.m[list[0].ID].LastRun == 0 {
		t.Fatal("run state not persisted")
	}
}
