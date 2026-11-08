package hub

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"strawlog/internal/agent"
	"strawlog/internal/api"
	"strawlog/internal/tlsutil"
)

// infoNode answers Info only; enough to pair and probe.
type infoNode struct{ api.Node }

func (infoNode) Info(context.Context) (api.Info, error) { return api.Info{Hostname: "tunnelled"}, nil }

// The agent listens on loopback only and is reached exclusively through SSH.
func TestPairThroughSSH(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	agentAddr := ln.Addr().String()
	ln.Close()
	dir := t.TempDir()
	code, err := agent.NewPairingCode(dir)
	if err != nil {
		t.Fatal(err)
	}
	fp, tok, _ := agent.ParsePairingCode(code)
	srv, err := agent.New(agent.Config{Listen: agentAddr, DataDir: dir, Node: infoNode{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Run(ctx)

	hd := t.TempDir()
	own, err := tlsutil.LoadOrCreate(filepath.Join(hd, "c.pem"), filepath.Join(hd, "k.pem"), "hub")
	if err != nil {
		t.Fatal(err)
	}
	sshAddr, hostFP := testSSHServer(t, nil)
	tn := newSSHTunnel(SSHConf{Host: sshAddr, User: "u", Password: "pw"})
	rem := NewRemote(agentAddr, own, fp, tn)
	defer rem.Close()

	c, done := context.WithTimeout(ctx, 10*time.Second)
	defer done()
	for { // wait for the agent listener
		if conn, err := net.Dial("tcp", agentAddr); err == nil {
			conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := rem.Pair(c, tok, "hub@test"); err != nil {
		t.Fatalf("pair through ssh: %v", err)
	}
	info, err := rem.Info(c)
	if err != nil || info.Hostname != "tunnelled" {
		t.Fatalf("info through ssh: %+v %v", info, err)
	}
	if tn.HostKey() != hostFP {
		t.Fatalf("host key not pinned")
	}
}
