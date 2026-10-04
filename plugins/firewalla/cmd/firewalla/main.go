// Command firewalla is both the gateway plugin (`firewalla serve`) and a CLI
// (`firewalla <noun> <verb>`). Both run the same operation table.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ByteDeskAI/remote-gateway-plugins/kit"
	"github.com/ByteDeskAI/remote-gateway-plugins/plugins/firewalla/internal/box"
	"github.com/ByteDeskAI/remote-gateway-plugins/plugins/firewalla/internal/msp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// config is everything the operations need. In CLI mode it comes from the
// environment; in serve mode from the gateway settings section.
type config struct {
	MSPDomain, MSPToken, BoxHost string
	Writes                       bool
}

func envConfig() config {
	w, _ := strconv.ParseBool(os.Getenv("FIREWALLA_ENABLE_WRITES"))
	return config{
		MSPDomain: os.Getenv("FIREWALLA_MSP_DOMAIN"),
		MSPToken:  os.Getenv("FIREWALLA_MSP_TOKEN"),
		BoxHost:   os.Getenv("FIREWALLA_BOX_HOST"),
		Writes:    w,
	}
}

// newTable builds the operation table. cfg is read on every call so a
// settings change applies without a restart.
func newTable(cfg func() config, auth func() []ssh.AuthMethod, knownHosts string, pub func() ssh.PublicKey) *kit.Table {
	ops := msp.Ops(func() *msp.Client {
		c := cfg()
		return &msp.Client{Domain: c.MSPDomain, Token: c.MSPToken}
	})
	ops = append(ops, box.Ops(func() *box.Box {
		return &box.Box{Host: cfg().BoxHost, Auth: auth(), KnownHosts: knownHosts}
	}, pub)...)
	return &kit.Table{Prefix: "firewalla", Ops: ops, WritesEnabled: func() bool { return cfg().Writes }}
}

const extraUsage = `
Other commands:
  serve                                       Run as a ByteDesk gateway plugin
  ssh bootstrap --host=IP --pubkey-file=FILE  Install an SSH key for user pi; reads the
                                              one-time pi password (Firewalla app) from stdin

Environment (CLI mode):
  FIREWALLA_MSP_DOMAIN     MSP domain, e.g. mycompany.firewalla.net
  FIREWALLA_MSP_TOKEN      MSP API personal access token
  FIREWALLA_BOX_HOST       Box LAN address for SSH reads
  FIREWALLA_ENABLE_WRITES  true to allow write operations (each still needs --confirm)
  SSH_AUTH_SOCK            ssh-agent holding the key installed by ssh bootstrap
  FIREWALLA_STATE_DIR      Where the SSH known_hosts file lives (default: user config dir)
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "serve" {
		if err := serve(ctx); err != nil {
			fmt.Fprintln(stderr, "firewalla serve:", err)
			return 1
		}
		return 0
	}
	knownHosts := filepath.Join(stateDir(), "known_hosts")
	if len(args) >= 2 && args[0] == "ssh" && args[1] == "bootstrap" {
		return bootstrap(ctx, args[2:], stdin, stdout, stderr, knownHosts)
	}
	t := newTable(envConfig, agentAuth, knownHosts, func() ssh.PublicKey { return nil })
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		w, code := stderr, 2
		if len(args) > 0 {
			w, code = stdout, 0
		}
		t.Usage("firewalla", w)
		fmt.Fprint(w, extraUsage)
		return code
	}
	return t.RunCLI(ctx, "firewalla", args, stdout, stderr)
}

func stateDir() string {
	if d := os.Getenv("FIREWALLA_STATE_DIR"); d != "" {
		return d
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "bytedesk-firewalla")
	}
	return ".firewalla"
}

// agentAuth uses the keys in ssh-agent. The agent is dialled only when an SSH
// operation actually runs.
func agentAuth() []ssh.AuthMethod {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}
	return []ssh.AuthMethod{ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			return nil, fmt.Errorf("ssh-agent: %w", err)
		}
		return agent.NewClient(conn).Signers()
	})}
}

func bootstrap(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, knownHosts string) int {
	var host, pubFile string
	for _, a := range args {
		k, v, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch k {
		case "host":
			host = v
		case "pubkey-file":
			pubFile = v
		default:
			fmt.Fprintf(stderr, "firewalla ssh bootstrap: unknown flag %q\n", a)
			return 2
		}
	}
	if host == "" {
		host = os.Getenv("FIREWALLA_BOX_HOST")
	}
	if host == "" || pubFile == "" {
		fmt.Fprintln(stderr, "usage: firewalla ssh bootstrap --host=IP --pubkey-file=FILE  (pi password on stdin)")
		return 2
	}
	raw, err := os.ReadFile(pubFile)
	if err != nil {
		fmt.Fprintln(stderr, "firewalla ssh bootstrap:", err)
		return 2
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		fmt.Fprintln(stderr, "firewalla ssh bootstrap: not an SSH public key:", err)
		return 2
	}
	fmt.Fprintln(stderr, "Enter the pi SSH password shown in the Firewalla app (SSH Console screen):")
	password, _ := bufio.NewReader(stdin).ReadString('\n')
	if err := box.Bootstrap(ctx, host, password, pub, knownHosts); err != nil {
		fmt.Fprintln(stderr, "firewalla ssh bootstrap:", err)
		return 1
	}
	line := strings.TrimSpace(string(box.AuthorizedKey(pub)))
	fmt.Fprintf(stdout, "Installed key %s for pi@%s, and %s to restore it after reboots.\n\nTo undo, run:\n  ssh pi@%s %q\n",
		ssh.FingerprintSHA256(pub), host, box.HookPath, host, box.RollbackScript(line))
	return 0
}
