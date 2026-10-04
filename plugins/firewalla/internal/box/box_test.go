package box

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// sshServer answers every exec with reply(cmd, stdin) and exit status 0.
func sshServer(t *testing.T, hostKey ssh.Signer, password string, reply func(cmd, stdin string) string) string {
	t.Helper()
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if string(p) == password {
			return nil, nil
		}
		return nil, io.EOF
	}}
	cfg.AddHostKey(hostKey)
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
					ch, creqs, _ := nc.Accept()
					go func() {
						for r := range creqs {
							if r.Type != "exec" {
								r.Reply(false, nil)
								continue
							}
							r.Reply(true, nil)
							cmd := string(r.Payload[4:])
							var in []byte
							if strings.HasPrefix(cmd, "sh -s") {
								in, _ = io.ReadAll(ch)
							}
							io.WriteString(ch, reply(cmd, string(in)))
							status := make([]byte, 4)
							binary.BigEndian.PutUint32(status, 0)
							ch.SendRequest("exit-status", false, status)
							ch.Close()
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func signer(t *testing.T) ssh.Signer {
	_, k, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTOFUAndReads(t *testing.T) {
	dir := t.TempDir()
	known := filepath.Join(dir, "known_hosts")
	addr := sshServer(t, signer(t), "pw", func(cmd, _ string) string {
		switch {
		case cmd == "ip -j addr":
			return `[{"ifname":"br0"}]`
		case strings.Contains(cmd, "dnsmasq.leases"):
			return "#path /home/pi/.router/run/dhcp/dnsmasq.leases\n1700000000 aa:bb:cc:dd:ee:ff 192.168.1.20 tv 01:aa\n1700000001 11:22:33:44:55:66 192.168.1.21 * *\nbad line\n"
		}
		return ""
	})
	b := &Box{Host: addr, KnownHosts: known, Auth: []ssh.AuthMethod{ssh.Password("pw")}}
	ops := Ops(func() *Box { return b }, func() ssh.PublicKey { return nil })
	out, err := ops[0].Handle(context.Background(), nil)
	if err != nil || string(out.(json.RawMessage)) != `[{"ifname":"br0"}]` {
		t.Fatalf("interfaces = %v, %v", out, err)
	}
	if raw, _ := os.ReadFile(known); !strings.Contains(string(raw), "ssh-ed25519") {
		t.Fatalf("host key not recorded: %q", raw)
	}
	leases, err := ops[2].Handle(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := leases.(map[string]any)
	if got["path"] != "/home/pi/.router/run/dhcp/dnsmasq.leases" || len(got["leases"].([]Lease)) != 2 || got["leases"].([]Lease)[1].Hostname != "" {
		t.Fatalf("leases = %+v", got)
	}

	// Same address, different host key: refused.
	addr2 := sshServer(t, signer(t), "pw", func(string, string) string { return "[]" })
	raw, _ := os.ReadFile(known)
	host2, port2, _ := net.SplitHostPort(addr2)
	_, port1, _ := net.SplitHostPort(addr)
	os.WriteFile(known, []byte(strings.ReplaceAll(string(raw), port1, port2)), 0o600)
	b.Host = host2 + ":" + port2
	if _, err := b.Run(context.Background(), "true", nil); err == nil || !strings.Contains(err.Error(), "host key") {
		t.Fatalf("changed key accepted: %v", err)
	}
}

func TestBootstrap(t *testing.T) {
	dir := t.TempDir()
	key, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "id_ed25519")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v", fi.Mode())
	}
	again, _ := LoadOrCreateKey(dir)
	if string(again.PublicKey().Marshal()) != string(key.PublicKey().Marshal()) {
		t.Fatal("key regenerated")
	}
	var script string
	addr := sshServer(t, signer(t), "secret", func(cmd, stdin string) string { script = stdin; return "bootstrap-ok\n" })
	if err := Bootstrap(context.Background(), addr, "wrong\n", key.PublicKey(), filepath.Join(dir, "kh")); err == nil {
		t.Fatal("wrong password accepted")
	}
	if err := Bootstrap(context.Background(), addr, "secret\n", key.PublicKey(), filepath.Join(dir, "kh")); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(AuthorizedKey(key.PublicKey())))
	for _, want := range []string{"KEY='" + line + "'", HookPath, `grep -qxF "$KEY"`, "chmod 755"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	if rb := RollbackScript(line); !strings.Contains(rb, "rm -f "+HookPath) || !strings.Contains(rb, line) {
		t.Fatal(rb)
	}
}

func TestNotConfigured(t *testing.T) {
	if _, err := (&Box{}).Run(context.Background(), "true", nil); err != ErrNotConfigured {
		t.Fatal(err)
	}
}
