package core

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Docker is a tiny read-only Docker Engine API client. It only uses
// GET /containers/json, /containers/{id}/json and /containers/{id}/logs,
// so it works behind a socket proxy restricted to CONTAINERS=1.
type Docker struct {
	hc   *http.Client
	base string
}

func NewDocker(host string) (*Docker, error) {
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{MaxIdleConns: 4, IdleConnTimeout: 60 * time.Second}
	base := ""
	switch u.Scheme {
	case "unix":
		sock := u.Path
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}
		base = "http://docker"
	case "tcp", "http":
		base = "http://" + u.Host
	default:
		return nil, fmt.Errorf("unsupported DOCKER_HOST scheme %q", u.Scheme)
	}
	return &Docker{hc: &http.Client{Transport: tr}, base: base}, nil
}

func (d *Docker) get(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", d.base+path, nil)
	resp, err := d.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type containerSummary struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
}

type Mount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

type ContainerJSON struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	LogPath string `json:"LogPath"`
	State   struct {
		Status string `json:"Status"`
	} `json:"State"`
	Config struct {
		Tty    bool              `json:"Tty"`
		Labels map[string]string `json:"Labels"`
		Image  string            `json:"Image"`
	} `json:"Config"`
	HostConfig struct {
		LogConfig struct {
			Type   string            `json:"Type"`
			Config map[string]string `json:"Config"`
		} `json:"LogConfig"`
	} `json:"HostConfig"`
	Mounts []Mount `json:"Mounts"`
}

func (d *Docker) Containers(ctx context.Context) ([]ContainerJSON, error) {
	var list []containerSummary
	if err := d.get(ctx, "/containers/json?all=1", &list); err != nil {
		return nil, err
	}
	out := make([]ContainerJSON, 0, len(list))
	for _, c := range list {
		var cj ContainerJSON
		if err := d.get(ctx, "/containers/"+c.ID+"/json", &cj); err != nil {
			continue
		}
		cj.Name = strings.TrimPrefix(cj.Name, "/")
		out = append(out, cj)
	}
	return out, nil
}

// Logs streams container logs. tail<0 means "all".
func (d *Docker) Logs(ctx context.Context, id string, tty bool, tail int, follow bool, since string, emit func(stream, ts, text string) error) error {
	q := url.Values{}
	q.Set("stdout", "1")
	q.Set("stderr", "1")
	q.Set("timestamps", "1")
	if tail >= 0 {
		q.Set("tail", strconv.Itoa(tail))
	} else {
		q.Set("tail", "all")
	}
	if follow {
		q.Set("follow", "1")
	}
	if since != "" {
		q.Set("since", since)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", d.base+"/containers/"+id+"/logs?"+q.Encode(), nil)
	resp, err := d.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker logs: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	split := func(stream string, line string) error {
		line = strings.TrimRight(line, "\r\n")
		ts, text, ok := strings.Cut(line, " ")
		if !ok {
			text, ts = ts, ""
		}
		return emit(stream, ts, text)
	}
	if tty {
		br := bufio.NewReaderSize(resp.Body, 64<<10)
		for {
			l, err := br.ReadString('\n')
			if l != "" {
				if e := split("stdout", l); e != nil {
					return e
				}
			}
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
		}
	}
	// multiplexed stream: [stream, 0,0,0, size uint32 BE] payload
	hdr := make([]byte, 8)
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	partial := map[byte]string{}
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			return err
		}
		stream := "stdout"
		if hdr[0] == 2 {
			stream = "stderr"
		}
		data := partial[hdr[0]] + string(buf)
		for {
			i := strings.IndexByte(data, '\n')
			if i < 0 {
				break
			}
			if e := split(stream, data[:i]); e != nil {
				return e
			}
			data = data[i+1:]
		}
		partial[hdr[0]] = data
	}
}
