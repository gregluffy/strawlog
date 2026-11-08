package hub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// testSSHServer accepts password "pw" or the given client key, and forwards direct-tcpip channels.
func testSSHServer(t *testing.T, clientKey ssh.PublicKey) (addr string, hostFP string) {
	t.Helper()
	_, hk, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(hk)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == "u" && string(pw) == "pw" {
				return nil, nil
			}
			return nil, io.EOF
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if clientKey != nil && string(k.Marshal()) == string(clientKey.Marshal()) {
				return nil, nil
			}
			return nil, io.EOF
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					if nc.ChannelType() != "direct-tcpip" {
						nc.Reject(ssh.UnknownChannelType, "no")
						continue
					}
					var p struct {
						Host  string
						Port  uint32
						OHost string
						OPort uint32
					}
					_ = ssh.Unmarshal(nc.ExtraData(), &p)
					dst, err := net.Dial("tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
					if err != nil {
						nc.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					ch, rq, _ := nc.Accept()
					go ssh.DiscardRequests(rq)
					go func() { io.Copy(ch, dst); ch.Close() }()
					go func() { io.Copy(dst, ch); dst.Close() }()
				}
			}()
		}
	}()
	return ln.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey())
}

func echoServer(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

func roundTrip(t *testing.T, tn *sshTunnel, target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := tn.DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	defer c.Close()
	c.Write([]byte("ping"))
	b := make([]byte, 4)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "ping" {
		t.Fatalf("echo through tunnel: %q %v", b, err)
	}
	return nil
}

func TestSSHTunnelPassword(t *testing.T) {
	sshAddr, fp := testSSHServer(t, nil)
	target := echoServer(t)

	tn := newSSHTunnel(SSHConf{Host: sshAddr, User: "u", Password: "pw"})
	defer tn.Close()
	if err := roundTrip(t, tn, target); err != nil {
		t.Fatal(err)
	}
	if tn.HostKey() != fp {
		t.Fatalf("pinned %q, want %q", tn.HostKey(), fp)
	}
	// connection is reused, and survives a dropped SSH client
	tn.drop(tn.cl)
	if err := roundTrip(t, tn, target); err != nil {
		t.Fatalf("after reconnect: %v", err)
	}

	bad := newSSHTunnel(SSHConf{Host: sshAddr, User: "u", Password: "nope"})
	defer bad.Close()
	if err := roundTrip(t, bad, target); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("wrong password: %v", err)
	}

	moved := newSSHTunnel(SSHConf{Host: sshAddr, User: "u", Password: "pw", HostKey: "SHA256:something-else"})
	defer moved.Close()
	if err := roundTrip(t, moved, target); err == nil || !strings.Contains(err.Error(), "host key changed") {
		t.Fatalf("host key mismatch: %v", err)
	}
}

func TestSSHTunnelKey(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	sshAddr, _ := testSSHServer(t, sshPub)
	target := echoServer(t)

	blk, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	key := string(pem.EncodeToMemory(blk))

	if _, err := (SSHConf{Host: sshAddr, User: "u", Key: key}).clientConfig(new(string)); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("missing passphrase: %v", err)
	}
	tn := newSSHTunnel(SSHConf{Host: sshAddr, User: "u", Key: key, Passphrase: "secret"})
	defer tn.Close()
	if err := roundTrip(t, tn, target); err != nil {
		t.Fatal(err)
	}
}

func TestBuildSSHKeepsCredentials(t *testing.T) {
	prev := &SSHConf{Host: "srv:22", User: "u", Password: "pw", HostKey: "SHA256:x"}
	c, err := buildSSH(&sshReq{Host: "srv", User: "u"}, prev)
	if err != nil || c.Password != "pw" || c.HostKey != "SHA256:x" {
		t.Fatalf("same host: %+v %v", c, err)
	}
	c, err = buildSSH(&sshReq{Host: "other", User: "u"}, prev)
	if err != nil || c.HostKey != "" || c.Host != "other:22" {
		t.Fatalf("new host must re-pin: %+v %v", c, err)
	}
	if _, err := buildSSH(&sshReq{Host: "srv", User: "u"}, nil); err == nil {
		t.Fatal("no credentials should fail")
	}
}
