package hub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"strawlog/internal/api"
	"strawlog/internal/tlsutil"
)

// Remote talks to an agent over pinned mutual TLS. It implements api.Node.
type Remote struct {
	base   string
	hc     *http.Client
	tunnel *sshTunnel // nil for a direct connection
}

// NewRemote connects directly to addr, or through tunnel when it is non-nil
// (addr is then the agent address as seen from the SSH server).
func NewRemote(addr string, own tls.Certificate, pin []byte, tunnel *sshTunnel) *Remote {
	dial := (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	if tunnel != nil {
		dial = tunnel.DialContext
	}
	tr := &http.Transport{
		TLSClientConfig:       tlsutil.PinnedClient(own, pin),
		ForceAttemptHTTP2:     true,
		DialContext:           dial,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 90 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
	}
	return &Remote{base: "https://" + addr, hc: &http.Client{Transport: tr}, tunnel: tunnel}
}

// Close drops pooled connections and the SSH tunnel, if any.
func (r *Remote) Close() {
	r.hc.CloseIdleConnections()
	if r.tunnel != nil {
		r.tunnel.Close()
	}
}

func (r *Remote) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return cleanErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func cleanErr(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			return fmt.Errorf("agent unreachable: %v", inner)
		}
	}
	return err
}

func (r *Remote) Pair(ctx context.Context, token []byte, name string) (api.Info, error) {
	var info api.Info
	err := r.do(ctx, "POST", "/v1/pair", map[string]string{"token": base64.RawURLEncoding.EncodeToString(token), "name": name}, &info)
	return info, err
}

func (r *Remote) Info(ctx context.Context) (v api.Info, err error) {
	err = r.do(ctx, "GET", "/v1/info", nil, &v)
	return
}
func (r *Remote) Sources(ctx context.Context, refresh bool) (v []api.Source, err error) {
	p := "/v1/sources"
	if refresh {
		p += "?refresh=1"
	}
	err = r.do(ctx, "GET", p, nil, &v)
	return
}
func (r *Remote) Tail(ctx context.Context, q api.TailReq) (v []api.Line, err error) {
	err = r.do(ctx, "POST", "/v1/tail", q, &v)
	return
}
func (r *Remote) Search(ctx context.Context, q api.SearchReq) (v api.SearchResult, err error) {
	err = r.do(ctx, "POST", "/v1/search", q, &v)
	return
}
func (r *Remote) Health(ctx context.Context) (v []api.Finding, err error) {
	err = r.do(ctx, "GET", "/v1/health", nil, &v)
	return
}
func (r *Remote) History(ctx context.Context) (v api.History, err error) {
	err = r.do(ctx, "GET", "/v1/history", nil, &v)
	return
}
func (r *Remote) Policies(ctx context.Context) (v []api.Policy, err error) {
	err = r.do(ctx, "GET", "/v1/policies", nil, &v)
	return
}
func (r *Remote) SetPolicies(ctx context.Context, p []api.Policy) error {
	return r.do(ctx, "PUT", "/v1/policies", p, nil)
}
func (r *Remote) Plan(ctx context.Context, q api.PlanReq) (v api.Plan, err error) {
	err = r.do(ctx, "POST", "/v1/plan", q, &v)
	return
}
func (r *Remote) Apply(ctx context.Context, q api.PlanReq) (v api.Plan, err error) {
	err = r.do(ctx, "POST", "/v1/apply", q, &v)
	return
}
func (r *Remote) Runs(ctx context.Context) (v []api.Plan, err error) {
	err = r.do(ctx, "GET", "/v1/runs", nil, &v)
	return
}

func (r *Remote) Follow(ctx context.Context, q api.TailReq, emit func(api.Line) error) error {
	b, _ := json.Marshal(q)
	req, err := http.NewRequestWithContext(ctx, "POST", r.base+"/v1/follow", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.hc.Do(req)
	if err != nil {
		return cleanErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("agent: %s", resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var l api.Line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if err := emit(l); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return sc.Err()
}
