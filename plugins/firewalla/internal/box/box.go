// Package box reads a Firewalla box over SSH as user pi. Firewalla does not
// document SSH access as an API: everything here is unofficial and may change
// with firmware. Only read operations exist; network and DHCP writes wait for
// the on-box spike (see the plugin README).
package box

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ByteDeskAI/remote-gateway-plugins/kit"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrNotConfigured is returned when no box host is set.
var ErrNotConfigured = errors.New("Firewalla box is not configured: set the box host (FIREWALLA_BOX_HOST)")

// Box is one SSH target.
type Box struct {
	Host string // host or host:port
	User string // default "pi"
	Auth []ssh.AuthMethod
	// KnownHosts is the trust-on-first-use file. The first key seen for Host is
	// recorded; any later different key is refused.
	KnownHosts string
}

func (b *Box) addr() string {
	if _, _, err := net.SplitHostPort(b.Host); err == nil {
		return b.Host
	}
	return net.JoinHostPort(b.Host, "22")
}

// Run executes cmd with stdin and returns stdout. A non-zero exit is an error
// that includes stderr.
func (b *Box) Run(ctx context.Context, cmd string, stdin io.Reader) ([]byte, error) {
	if b == nil || strings.TrimSpace(b.Host) == "" {
		return nil, ErrNotConfigured
	}
	if len(b.Auth) == 0 {
		return nil, errors.New("no SSH credentials: load the key into ssh-agent (SSH_AUTH_SOCK) or run inside the gateway")
	}
	user := b.User
	if user == "" {
		user = "pi"
	}
	cfg := &ssh.ClientConfig{User: user, Auth: b.Auth, HostKeyCallback: TOFU(b.KnownHosts), Timeout: 15 * time.Second}
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", b.addr())
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %w", b.addr(), err)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	c, chans, reqs, err := ssh.NewClientConn(conn, b.addr(), cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh %s: %w", b.addr(), err)
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()
	s, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var out, errOut bytes.Buffer
	s.Stdout, s.Stderr, s.Stdin = &out, &errOut, stdin
	if err := s.Run(cmd); err != nil {
		return nil, fmt.Errorf("ssh %s: %v: %s", b.addr(), err, strings.TrimSpace(errOut.String()))
	}
	return out.Bytes(), nil
}

// TOFU trusts the first host key seen for a host and verifies it thereafter.
func TOFU(path string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if path == "" {
			return errors.New("no known-hosts file configured; refusing an unverified host key")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
		if err != nil {
			return err
		}
		f.Close()
		check, err := knownhosts.New(path)
		if err != nil {
			return err
		}
		err = check(hostname, remote, key)
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) && len(ke.Want) == 0 {
			af, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			defer af.Close()
			_, err = fmt.Fprintln(af, knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key))
			return err
		}
		if errors.As(err, &ke) {
			return fmt.Errorf("host key for %s changed (fingerprint %s); if the box was reset, delete its line from %s and retry", hostname, ssh.FingerprintSHA256(key), path)
		}
		return err
	}
}

// LoadOrCreateKey returns the plugin's ed25519 key from dir/id_ed25519,
// generating it (mode 0600) on first use.
func LoadOrCreateKey(dir string) (ssh.Signer, error) {
	path := filepath.Join(dir, "id_ed25519")
	if raw, err := os.ReadFile(path); err == nil {
		return ssh.ParsePrivateKey(raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "bytedesk-firewalla")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path+".pub", AuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		return nil, err
	}
	return signer, nil
}

// AuthorizedKey is the one authorized_keys line for key.
func AuthorizedKey(key ssh.PublicKey) []byte {
	return append(bytes.TrimSpace(ssh.MarshalAuthorizedKey(key)), []byte(" bytedesk-firewalla\n")...)
}

// HookPath re-adds the key after reboots and firmware upgrades. Firewalla runs
// every script in post_main.d after its main service starts.
const HookPath = "/home/pi/.firewalla/config/post_main.d/10-bytedesk-authorized-keys.sh"

// BootstrapScript installs keyLine for pi now and on every boot. keyLine must
// come from AuthorizedKey, which only contains base64, a key type and a fixed
// comment, so single-quoting it is safe.
func BootstrapScript(keyLine string) string {
	keyLine = strings.TrimSpace(keyLine)
	add := `mkdir -p /home/pi/.ssh && chmod 700 /home/pi/.ssh
touch /home/pi/.ssh/authorized_keys && chmod 600 /home/pi/.ssh/authorized_keys
grep -qxF "$KEY" /home/pi/.ssh/authorized_keys || printf '%s\n' "$KEY" >> /home/pi/.ssh/authorized_keys
if [ "$(id -u)" = 0 ]; then chown -R pi:pi /home/pi/.ssh; fi`
	return `set -eu
KEY='` + keyLine + `'
` + add + `
mkdir -p "$(dirname ` + HookPath + `)"
cat > ` + HookPath + ` <<'BYTEDESK_EOF'
#!/bin/sh
# Installed by the ByteDesk Firewalla plugin. Re-adds its SSH key after reboots.
# Remove this file and the key line in /home/pi/.ssh/authorized_keys to undo.
KEY='` + keyLine + `'
` + add + `
BYTEDESK_EOF
chmod 755 ` + HookPath + `
echo bootstrap-ok
`
}

// RollbackScript undoes BootstrapScript.
func RollbackScript(keyLine string) string {
	return `rm -f ` + HookPath + ` && grep -vxF '` + strings.TrimSpace(keyLine) + `' /home/pi/.ssh/authorized_keys > /home/pi/.ssh/authorized_keys.new; mv /home/pi/.ssh/authorized_keys.new /home/pi/.ssh/authorized_keys && chmod 600 /home/pi/.ssh/authorized_keys`
}

// Bootstrap logs in with the one-time pi password and installs pub.
func Bootstrap(ctx context.Context, host, password string, pub ssh.PublicKey, knownHosts string) error {
	pw := strings.TrimRight(password, "\r\n")
	if pw == "" {
		return errors.New("empty password")
	}
	b := &Box{Host: host, KnownHosts: knownHosts, Auth: []ssh.AuthMethod{
		ssh.Password(pw),
		ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			out := make([]string, len(qs))
			for i := range out {
				out[i] = pw
			}
			return out, nil
		}),
	}}
	out, err := b.Run(ctx, "sh -s", strings.NewReader(BootstrapScript(string(AuthorizedKey(pub)))))
	if err != nil {
		return err
	}
	if !bytes.Contains(out, []byte("bootstrap-ok")) {
		return fmt.Errorf("bootstrap did not confirm success: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// LeaseFiles are the dnsmasq lease paths tried in order. UNVERIFIED until the
// on-box spike: the first is where Firewalla's dnsmasq is believed to write.
var LeaseFiles = []string{"/home/pi/.router/run/dhcp/dnsmasq.leases", "/var/lib/misc/dnsmasq.leases"}

// Lease is one dnsmasq lease line: "<expiry> <mac> <ip> <hostname> <client-id>".
type Lease struct {
	Expiry   int64  `json:"expiry"`
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	ClientID string `json:"clientId"`
}

// ParseLeases reads dnsmasq lease lines, skipping malformed ones.
func ParseLeases(raw []byte) []Lease {
	out := []Lease{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || strings.HasPrefix(f[0], "#") {
			continue
		}
		var exp int64
		if _, err := fmt.Sscan(f[0], &exp); err != nil {
			continue
		}
		l := Lease{Expiry: exp, MAC: f[1], IP: f[2], Hostname: f[3]}
		if len(f) > 4 {
			l.ClientID = f[4]
		}
		if l.Hostname == "*" {
			l.Hostname = ""
		}
		out = append(out, l)
	}
	return out
}

func leasesCommand() string {
	return `for f in ` + strings.Join(LeaseFiles, " ") + `; do if [ -r "$f" ]; then echo "#path $f"; cat "$f"; exit 0; fi; done; echo "no dnsmasq lease file found in: ` + strings.Join(LeaseFiles, " ") + `" >&2; exit 3`
}

// versionCommand reports what identifies the firmware. UNVERIFIED on a real
// box: /home/pi/firewalla is believed to be the Firewalla git checkout.
const versionCommand = `cd /home/pi/firewalla 2>/dev/null && echo "branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null)" && echo "commit=$(git rev-parse --short HEAD 2>/dev/null)"; echo "kernel=$(uname -r)"; echo "model=$(tr -d '\0' </proc/device-tree/model 2>/dev/null)"`

// Ops is the SSH half of the operation table. box is resolved per call.
// pubkey reports the key the plugin authenticates with (nil when unknown).
func Ops(box func() *Box, pubkey func() ssh.PublicKey) []kit.Op {
	runJSON := func(cmd string) func(context.Context, json.RawMessage) (any, error) {
		return func(ctx context.Context, _ json.RawMessage) (any, error) {
			out, err := box().Run(ctx, cmd, nil)
			if err != nil {
				return nil, err
			}
			if !json.Valid(out) {
				return nil, errors.New("box returned non-JSON output; is iproute2 new enough for -j?")
			}
			return json.RawMessage(out), nil
		}
	}
	return []kit.Op{
		{Name: "box.interfaces", Summary: "Interfaces and addresses on the box (ip -j addr, over SSH)", Risk: kit.Read, Handle: runJSON("ip -j addr")},
		{Name: "box.routes", Summary: "Routing table on the box (ip -j route, over SSH)", Risk: kit.Read, Handle: runJSON("ip -j route")},
		{Name: "box.leases", Summary: "DHCP leases from the box's dnsmasq lease file (over SSH, unofficial)", Risk: kit.Read, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
			out, err := box().Run(ctx, leasesCommand(), nil)
			if err != nil {
				return nil, err
			}
			path, _, _ := strings.Cut(strings.TrimPrefix(string(out), "#path "), "\n")
			return map[string]any{"path": path, "leases": ParseLeases(out)}, nil
		}},
		{Name: "box.version", Summary: "Firmware branch and commit, kernel and model (over SSH, unofficial)", Risk: kit.Read, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
			out, err := box().Run(ctx, versionCommand, nil)
			if err != nil {
				return nil, err
			}
			info := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if k, v, ok := strings.Cut(line, "="); ok {
					info[k] = v
				}
			}
			return info, nil
		}},
		{Name: "ssh.pubkey", Summary: "The public key this plugin uses for SSH, and how to install it on the box", Risk: kit.Read, Handle: func(context.Context, json.RawMessage) (any, error) {
			key := pubkey()
			if key == nil {
				return nil, errors.New("no plugin key: in CLI mode the key comes from ssh-agent; use your own .pub file with `firewalla ssh bootstrap`")
			}
			line := strings.TrimSpace(string(AuthorizedKey(key)))
			return map[string]string{
				"publicKey":   line,
				"fingerprint": ssh.FingerprintSHA256(key),
				"bootstrap":   "firewalla ssh bootstrap --host=<box-ip> --pubkey-file=<file containing the publicKey line>   (reads the pi password from the Firewalla app on stdin)",
				"rollback":    RollbackScript(line),
			}, nil
		}},
	}
}
