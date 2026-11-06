package core

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"strawlog/internal/api"
)

const (
	maxTailLines = 5000
	maxLineBytes = 16 << 10 // lines longer than this are cut (keeps memory bounded)
)

var levelRe = regexp.MustCompile(`(?i)\b(fatal|panic|crit(?:ical)?|emerg|alert|err(?:or)?|warn(?:ing)?|info|notice|debug|trace)\b`)

func detectLevel(s string) string {
	if len(s) > 200 {
		s = s[:200]
	}
	m := levelRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	switch strings.ToLower(m[1])[:2] {
	case "fa", "pa", "cr", "em", "al", "er":
		return "error"
	case "wa":
		return "warn"
	case "in", "no":
		return "info"
	default:
		return "debug"
	}
}

func clampLines(n int) int {
	if n <= 0 {
		return 200
	}
	return min(n, maxTailLines)
}

func (c *Core) Tail(ctx context.Context, r api.TailReq) ([]api.Line, error) {
	s, cj, err := c.source(r.Source)
	if err != nil {
		return nil, err
	}
	n := clampLines(r.Lines)
	switch s.Kind {
	case api.KindJournal:
		return c.journalTail(ctx, s, n, r.Unit)
	case api.KindDocker:
		var out []api.Line
		err := c.docker.Logs(ctx, cj.ID, cj.Config.Tty, n, false, "", func(st, ts, text string) error {
			out = append(out, api.Line{Source: s.ID, Time: ts, Stream: st, Text: cut(text), Level: detectLevel(text)})
			return nil
		})
		return out, err
	default:
		path := s.Path
		if _, err := os.Stat(c.hp(path)); err != nil && len(s.Rotated) > 0 {
			path = s.Rotated[0].Path
		}
		lines, err := tailFile(c.hp(path), n)
		if err != nil {
			return nil, err
		}
		out := make([]api.Line, len(lines))
		for i, l := range lines {
			out[i] = api.Line{Source: s.ID, Text: l, Level: detectLevel(l)}
		}
		return out, nil
	}
}

func cut(s string) string {
	if len(s) > maxLineBytes {
		return s[:maxLineBytes] + " …[truncated]"
	}
	return s
}

// tailFile returns the last n lines, reading backwards in chunks.
func tailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if compressExt.MatchString(path) {
		return tailStream(f, path, n)
	}
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const chunk = 64 << 10
	end := st.Size()
	var buf []byte
	count := 0
	for end > 0 && count <= n {
		start := max(0, end-chunk)
		b := make([]byte, end-start)
		if _, err := f.ReadAt(b, start); err != nil && err != io.EOF {
			return nil, err
		}
		count += bytes.Count(b, []byte{'\n'})
		buf = append(b, buf...)
		end = start
		if len(buf) > 32<<20 {
			break
		}
	}
	buf = bytes.TrimRight(buf, "\n")
	parts := strings.Split(string(buf), "\n")
	if end > 0 && len(parts) > 0 {
		parts = parts[1:] // first is partial
	}
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	for i := range parts {
		parts[i] = cut(strings.TrimRight(parts[i], "\r"))
	}
	if len(parts) == 1 && parts[0] == "" {
		return nil, nil
	}
	return parts, nil
}

func tailStream(r io.Reader, path string, n int) ([]string, error) {
	rd, closeFn, err := openReader(r, path)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	ring := make([]string, 0, n)
	err = scanLines(rd, func(b []byte) error {
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, cut(string(b)))
		return nil
	})
	return ring, err
}

func openReader(r io.Reader, path string) (io.Reader, func(), error) {
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		return gz, func() { gz.Close() }, nil
	}
	if compressExt.MatchString(path) {
		return nil, nil, fmt.Errorf("unsupported compression for %s", path)
	}
	return r, func() {}, nil
}

// scanLines calls fn for every line (without newline). Long lines are cut.
func scanLines(r io.Reader, fn func([]byte) error) error {
	br := bufio.NewReaderSize(r, 256<<10)
	var long []byte
	for {
		b, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			if len(long) < maxLineBytes {
				long = append(long, b...)
			}
			continue
		}
		if len(long) > 0 {
			long = append(long, b...)
			b = long
		}
		if len(b) > 0 {
			b = bytes.TrimRight(b, "\r\n")
			if len(b) > maxLineBytes {
				b = b[:maxLineBytes]
			}
			if e := fn(b); e != nil {
				return e
			}
		}
		long = long[:0]
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

var errStop = errors.New("stop")

func (c *Core) Follow(ctx context.Context, r api.TailReq, emit func(api.Line) error) error {
	s, cj, err := c.source(r.Source)
	if err != nil {
		return err
	}
	n := clampLines(r.Lines)
	switch s.Kind {
	case api.KindJournal:
		return c.journalFollow(ctx, s, n, r.Unit, emit)
	case api.KindDocker:
		return c.docker.Logs(ctx, cj.ID, cj.Config.Tty, n, true, "", func(st, ts, text string) error {
			return emit(api.Line{Source: s.ID, Time: ts, Stream: st, Text: cut(text), Level: detectLevel(text)})
		})
	}
	initial, err := c.Tail(ctx, r)
	if err != nil {
		return err
	}
	for _, l := range initial {
		if err := emit(l); err != nil {
			return err
		}
	}
	return followFile(ctx, c.hp(s.Path), func(b []byte) error {
		t := string(b)
		return emit(api.Line{Source: s.ID, Text: cut(t), Level: detectLevel(t)})
	})
}

// followFile polls the file; handles truncation and rotation (inode change).
func followFile(ctx context.Context, path string, fn func([]byte) error) error {
	var f *os.File
	var ino uint64
	var off int64
	open := func(fromEnd bool) {
		if f != nil {
			f.Close()
			f = nil
		}
		nf, err := os.Open(path)
		if err != nil {
			return
		}
		st, _ := nf.Stat()
		f = nf
		ino = inode(st)
		off = 0
		if fromEnd {
			off = st.Size()
		}
	}
	open(true)
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	t := time.NewTicker(700 * time.Millisecond)
	defer t.Stop()
	var partial []byte
	buf := make([]byte, 64<<10)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		if f == nil || inode(st) != ino {
			open(false)
			partial = nil
			if f == nil {
				continue
			}
		} else if st.Size() < off {
			off = 0 // truncated
			partial = nil
		}
		for {
			k, err := f.ReadAt(buf, off)
			if k > 0 {
				off += int64(k)
				data := append(partial, buf[:k]...)
				for {
					i := bytes.IndexByte(data, '\n')
					if i < 0 {
						break
					}
					if e := fn(bytes.TrimRight(data[:i], "\r")); e != nil {
						return e
					}
					data = data[i+1:]
				}
				partial = append([]byte(nil), data...)
				if len(partial) > maxLineBytes {
					if e := fn(partial[:maxLineBytes]); e != nil {
						return e
					}
					partial = nil
				}
			}
			if err != nil || k < len(buf) {
				break
			}
		}
	}
}

func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

// ---------- docker json-file parsing ----------

type dockerJSONLine struct {
	Log    string `json:"log"`
	Stream string `json:"stream"`
	Time   string `json:"time"`
}

func parseDockerLine(b []byte) (api.Line, bool) {
	var d dockerJSONLine
	if json.Unmarshal(b, &d) != nil {
		return api.Line{}, false
	}
	t := strings.TrimRight(d.Log, "\r\n")
	return api.Line{Text: cut(t), Stream: d.Stream, Time: d.Time, Level: detectLevel(t)}, true
}

func atoiDef(s string, d int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return d
}
