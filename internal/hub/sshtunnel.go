package hub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHConf lets the hub reach an agent through the server's SSH daemon instead of
// exposing the agent port: the hub logs in with a password or private key and opens
// a forwarded TCP channel to the agent's address as seen from the server
// (usually 127.0.0.1:7443). mTLS pinning to the agent still runs inside the tunnel.
type SSHConf struct {
	Host       string `json:"host"` // host:port of the SSH server
	User       string `json:"user"`
	Password   string `json:"password,omitempty"`
	Key        string `json:"key,omitempty"` // PEM / OpenSSH private key
	Passphrase string `json:"passphrase,omitempty"`
	HostKey    string `json:"hostKey,omitempty"` // SHA256 fingerprint, pinned on first connect
}

func (c SSHConf) auth() string {
	if c.Key != "" {
		return "key"
	}
	return "password"
}

func normalizeSSHHost(h string) (string, error) {
	h = strings.TrimSpace(strings.TrimPrefix(h, "ssh://"))
	if i := strings.LastIndex(h, "@"); i >= 0 { // tolerate "user@host"
		h = h[i+1:]
	}
	if h == "" {
		return "", errors.New("SSH host is required")
	}
	if _, _, err := net.SplitHostPort(h); err != nil {
		h = net.JoinHostPort(strings.Trim(h, "[]"), "22")
	}
	return h, nil
}

func (c SSHConf) clientConfig(pinned *string) (*ssh.ClientConfig, error) {
	if c.User == "" {
		return nil, errors.New("SSH username is required")
	}
	var am []ssh.AuthMethod
	switch {
	case c.Key != "":
		var (
			signer ssh.Signer
			err    error
		)
		if c.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.Key), []byte(c.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(c.Key))
		}
		var pm *ssh.PassphraseMissingError
		if errors.As(err, &pm) {
			return nil, errors.New("the private key is encrypted; enter its passphrase")
		}
		if err != nil {
			return nil, fmt.Errorf("private key: %w", err)
		}
		am = append(am, ssh.PublicKeys(signer))
	case c.Password != "":
		pw := c.Password
		am = append(am, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			ans := make([]string, len(qs))
			for i := range ans {
				ans[i] = pw
			}
			return ans, nil
		}))
	default:
		return nil, errors.New("enter an SSH password or a private key")
	}
	return &ssh.ClientConfig{
		User:    c.User,
		Auth:    am,
		Timeout: 10 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp := ssh.FingerprintSHA256(key)
			if c.HostKey == "" { // first connect: trust and remember
				*pinned = fp
				return nil
			}
			if fp != c.HostKey {
				return fmt.Errorf("SSH host key changed (now %s, pinned %s); if the server was reinstalled, update the node's SSH connection", fp, c.HostKey)
			}
			*pinned = fp
			return nil
		},
	}, nil
}

// sshTunnel keeps one SSH connection per node and reconnects on demand.
type sshTunnel struct {
	conf   SSHConf
	mu     sync.Mutex
	cl     *ssh.Client
	pinned string
	closed bool
}

func newSSHTunnel(c SSHConf) *sshTunnel { return &sshTunnel{conf: c} }

// HostKey returns the server's host key fingerprint seen on the last successful connect.
func (t *sshTunnel) HostKey() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pinned
}

func (t *sshTunnel) client(ctx context.Context) (*ssh.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, errors.New("ssh tunnel closed")
	}
	if t.cl != nil {
		return t.cl, nil
	}
	var pinned string
	cfg, err := t.conf.clientConfig(&pinned)
	if err != nil {
		return nil, err
	}
	raw, err := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", t.conf.Host)
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %w", t.conf.Host, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(dl)
	} else {
		_ = raw.SetDeadline(time.Now().Add(15 * time.Second))
	}
	cc, chans, reqs, err := ssh.NewClientConn(raw, t.conf.Host, cfg)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("ssh %s: %w", t.conf.Host, sshErr(err))
	}
	_ = raw.SetDeadline(time.Time{})
	cl := ssh.NewClient(cc, chans, reqs)
	t.cl, t.pinned = cl, pinned
	go t.keepalive(cl)
	return cl, nil
}

func sshErr(err error) error {
	if s := err.Error(); strings.Contains(s, "unable to authenticate") {
		return errors.New("authentication failed (wrong username, password or key)")
	}
	return err
}

// keepalive detects dead connections so the next request reconnects instead of hanging.
func (t *sshTunnel) keepalive(cl *ssh.Client) {
	tk := time.NewTicker(30 * time.Second)
	defer tk.Stop()
	done := make(chan struct{})
	go func() { _ = cl.Wait(); close(done) }()
	for {
		select {
		case <-done:
			t.drop(cl)
			return
		case <-tk.C:
			res := make(chan error, 1)
			go func() { _, _, err := cl.SendRequest("keepalive@openssh.com", true, nil); res <- err }()
			select {
			case err := <-res:
				if err != nil {
					t.drop(cl)
					return
				}
			case <-time.After(15 * time.Second):
				t.drop(cl)
				return
			}
		}
	}
}

func (t *sshTunnel) drop(cl *ssh.Client) {
	t.mu.Lock()
	if t.cl == cl {
		t.cl = nil
	}
	t.mu.Unlock()
	cl.Close()
}

// DialContext opens a forwarded connection to addr (as seen from the SSH server).
func (t *sshTunnel) DialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	for attempt := 0; ; attempt++ {
		cl, err := t.client(ctx)
		if err != nil {
			return nil, err
		}
		c, err := cl.DialContext(ctx, "tcp", addr)
		if err == nil {
			return c, nil
		}
		var oc *ssh.OpenChannelError
		if errors.As(err, &oc) || attempt > 0 || ctx.Err() != nil {
			// the SSH server answered but refused: agent not listening there, or forwarding disabled
			return nil, fmt.Errorf("ssh tunnel to %s: %v (is the agent listening there, and is AllowTcpForwarding enabled?)", addr, err)
		}
		t.drop(cl) // stale connection: reconnect once
	}
}

func (t *sshTunnel) Close() {
	t.mu.Lock()
	cl := t.cl
	t.cl, t.closed = nil, true
	t.mu.Unlock()
	if cl != nil {
		cl.Close()
	}
}
