// Package api holds the wire types shared by the hub, the agent and the
// in-process core. Every node (local or remote) implements Node.
package api

import "context"

const Version = "0.1.0"

type Kind string

const (
	KindDocker  Kind = "docker"
	KindFile    Kind = "file"
	KindJournal Kind = "journal"
)

// Info describes a node.
type Info struct {
	Hostname     string   `json:"hostname"`
	Version      string   `json:"version"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Docker       bool     `json:"docker"`
	DockerError  string   `json:"dockerError,omitempty"`
	Journal      bool     `json:"journal"`
	HostRoot     string   `json:"hostRoot"`
	Timezone     string   `json:"timezone"` // node local zone, e.g. "UTC" or "Europe/Athens (+02:00)"
	AllowWrite   bool     `json:"allowWrite"`
	WritePaths   []string `json:"writePaths,omitempty"`
	Disks        []Disk   `json:"disks"`
	UptimeSec    int64    `json:"uptimeSec"`
	SourcesCount int      `json:"sourcesCount"`
	TotalBytes   int64    `json:"totalBytes"`
}

type Disk struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	Free  uint64 `json:"free"`
}

type FileInfo struct {
	Path       string `json:"path"` // host path
	Size       int64  `json:"size"`
	ModTime    int64  `json:"mtime"`
	Compressed bool   `json:"compressed,omitempty"`
}

// Source is one logical log stream (a container, a file + its rotations, the journal).
type Source struct {
	ID        string            `json:"id"`
	Kind      Kind              `json:"kind"`
	App       string            `json:"app"`
	Name      string            `json:"name"`
	Path      string            `json:"path,omitempty"` // host path of the active file
	Size      int64             `json:"size"`           // active file
	TotalSize int64             `json:"totalSize"`      // active + rotated
	ModTime   int64             `json:"mtime,omitempty"`
	Rotated   []FileInfo        `json:"rotated,omitempty"`
	Container string            `json:"container,omitempty"`
	State     string            `json:"state,omitempty"`
	LogDriver string            `json:"logDriver,omitempty"`
	LogOpts   map[string]string `json:"logOpts,omitempty"`
	Origin    string            `json:"origin,omitempty"` // "var-log", "bind", "volume", "label", "extra"
	Readable  bool              `json:"readable"`
	Writable  bool              `json:"writable"`
	Units     []string          `json:"units,omitempty"`
	Growth24h int64             `json:"growth24h"`
}

type Line struct {
	Source string `json:"s,omitempty"`
	Time   string `json:"t,omitempty"`
	Stream string `json:"st,omitempty"`
	Level  string `json:"l,omitempty"`
	Text   string `json:"m"`
	File   string `json:"f,omitempty"`
}

type TailReq struct {
	Source string `json:"source"`
	Lines  int    `json:"lines"`
	Unit   string `json:"unit,omitempty"`
}

type SearchReq struct {
	Sources        []string `json:"sources,omitempty"`
	Apps           []string `json:"apps,omitempty"`
	Query          string   `json:"query"`
	Regex          bool     `json:"regex"`
	CaseSensitive  bool     `json:"caseSensitive"`
	IncludeRotated bool     `json:"includeRotated"`
	Levels         []string `json:"levels,omitempty"`
	Unit           string   `json:"unit,omitempty"`
	Since          string   `json:"since,omitempty"` // journal/docker only, e.g. "2h" or RFC3339
	Limit          int      `json:"limit"`
}

type SearchResult struct {
	Lines        []Line   `json:"lines"`
	Matches      int64    `json:"matches"`
	Truncated    bool     `json:"truncated"`
	ScannedBytes int64    `json:"scannedBytes"`
	DurationMs   int64    `json:"durationMs"`
	Errors       []string `json:"errors,omitempty"`
}

type Finding struct {
	Key      string `json:"key"`      // stable identity (code + subject) for de-duplicating notifications
	Severity string `json:"severity"` // critical, warning, info
	Code     string `json:"code"`
	SourceID string `json:"sourceId,omitempty"`
	App      string `json:"app,omitempty"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
}

type Sample struct {
	T int64 `json:"t"`
	B int64 `json:"b"`
}

type History struct {
	Apps  map[string][]Sample `json:"apps"`
	Total []Sample            `json:"total"`
}

type Match struct {
	Apps     []string `json:"apps,omitempty"`
	Kinds    []Kind   `json:"kinds,omitempty"`
	Sources  []string `json:"sources,omitempty"`
	PathGlob string   `json:"pathGlob,omitempty"`
}

// Schedule says when an enabled policy runs on its own.
type Schedule struct {
	Mode     string `json:"mode"`               // "interval" or "daily"
	EveryMin int    `json:"everyMin,omitempty"` // interval: minutes between runs
	At       string `json:"at,omitempty"`       // daily: "HH:MM", node local time
}

type Policy struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Enabled               bool     `json:"enabled"` // runs automatically on Schedule; otherwise manual only
	Schedule              Schedule `json:"schedule"`
	Match                 Match    `json:"match"`
	MaxAgeDays            int      `json:"maxAgeDays,omitempty"`
	MaxRotated            int      `json:"maxRotated,omitempty"`
	CompressRotated       bool     `json:"compressRotated,omitempty"`
	MaxActiveMB           int64    `json:"maxActiveMB,omitempty"`
	ArchiveBeforeTruncate bool     `json:"archiveBeforeTruncate,omitempty"`
	MaxTotalMB            int64    `json:"maxTotalMB,omitempty"`

	// read-only, filled by the node
	LastRun int64 `json:"lastRun,omitempty"` // unix, last scheduled run
	NextRun int64 `json:"nextRun,omitempty"` // unix, 0 when disabled or node is read-only
}

type PlanReq struct {
	PolicyIDs []string `json:"policyIds,omitempty"` // empty = all enabled
	Policy    *Policy  `json:"policy,omitempty"`    // ad-hoc (preview of unsaved policy)
}

type Action struct {
	Policy   string `json:"policy"`
	SourceID string `json:"sourceId"`
	Path     string `json:"path"`
	Op       string `json:"op"` // delete, compress, truncate, archive-truncate, vacuum
	Bytes    int64  `json:"bytes"`
	Reason   string `json:"reason"`
	Error    string `json:"error,omitempty"`
	Done     bool   `json:"done"`
}

type Plan struct {
	DryRun     bool     `json:"dryRun"`
	Actions    []Action `json:"actions"`
	FreedBytes int64    `json:"freedBytes"`
	At         int64    `json:"at"`
	Trigger    string   `json:"trigger,omitempty"`
}

// Node is implemented by core.Core (in-process) and hub.Remote (over mTLS).
type Node interface {
	Info(ctx context.Context) (Info, error)
	Sources(ctx context.Context, refresh bool) ([]Source, error)
	Tail(ctx context.Context, r TailReq) ([]Line, error)
	Follow(ctx context.Context, r TailReq, emit func(Line) error) error
	Search(ctx context.Context, r SearchReq) (SearchResult, error)
	Health(ctx context.Context) ([]Finding, error)
	History(ctx context.Context) (History, error)
	Policies(ctx context.Context) ([]Policy, error)
	SetPolicies(ctx context.Context, p []Policy) error
	Plan(ctx context.Context, r PlanReq) (Plan, error)
	Apply(ctx context.Context, r PlanReq) (Plan, error)
	Runs(ctx context.Context) ([]Plan, error)
}
