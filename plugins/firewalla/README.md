# Firewalla plugin

Gives gateway users, AI agents and scripts access to their own Firewalla router. It uses the
official [Firewalla MSP API](https://docs.firewalla.net/) for boxes, devices, rules, alarms,
flows, statistics and target lists, and SSH to the box for what the API does not cover.

The same operations are available three ways:

| Surface | How to reach it |
|---|---|
| MCP tools | `firewalla_<noun>_<verb>`, for example `firewalla_devices_list` |
| HTTP | `GET` (reads) or `POST` (any) `/p/firewalla/api/<noun>/<verb>` |
| CLI | `firewalla <noun> <verb> [--json] [--confirm] [--key=value …]` |

Run `firewalla --help` for the full list.

## Install

1. In the gateway, open **Store**, find **Firewalla**, and install it.
2. Open **Settings → Firewalla** and fill in the fields below.

## Settings

| Setting | What to enter |
|---|---|
| MSP domain | Your MSP domain without `https://`, for example `mycompany.firewalla.net` |
| MSP API token | A personal access token (see below). Stored by the gateway, write-only |
| Box address | The box's LAN address for SSH reads, for example `192.168.1.1` |
| Enable write tools | Off by default. Allows MSP changes; each call still needs `confirm: true` |
| Enable SSH writes | Off by default. Reserved: no SSH write operations exist yet |

### Create an MSP API token

1. Sign in to your MSP portal (`https://<your-msp-domain>`).
2. Open **MSP Settings → MSP API**.
3. Create a personal access token and copy it once.
4. Paste it into the **MSP API token** setting, or export it as `FIREWALLA_MSP_TOKEN` for the CLI.

## CLI

```bash
export FIREWALLA_MSP_DOMAIN=mycompany.firewalla.net
export FIREWALLA_MSP_TOKEN=…            # from a secret store, never typed into history
export FIREWALLA_BOX_HOST=192.168.1.1   # for box.* commands; SSH key comes from ssh-agent
firewalla boxes list --json
firewalla flows list --query='box.id:<gid>' --limit=500 --maxPages=5
firewalla box leases
FIREWALLA_ENABLE_WRITES=true firewalla devices rename --gid=<gid> --id=<mac> --name=TV --confirm
```

## SSH bootstrap

SSH reads need a key installed for user `pi` on the box. Do this once:

1. In the Firewalla app, open the box's SSH Console screen and note the `pi` password.
2. Run, with the public key you want to install (in the gateway, the `firewalla_ssh_pubkey` tool shows the
   plugin's own key):
   ```bash
   firewalla ssh bootstrap --host=192.168.1.1 --pubkey-file=$HOME/.ssh/id_bytedesk.pub
   ```
   Type the password when asked. It is read from standard input and never stored.
3. The command adds the key to `/home/pi/.ssh/authorized_keys` and writes
   `/home/pi/.firewalla/config/post_main.d/10-bytedesk-authorized-keys.sh`, which adds it again
   after reboots and firmware upgrades. It then prints the exact rollback command.

The first SSH connection records the box's host key (`known_hosts` in the plugin state
directory, or `FIREWALLA_STATE_DIR` for the CLI). A different key later is refused. If you
reset the box, delete its line from that file.

## Safety model

- Read operations always run.
- Write and destructive operations are refused unless writes are enabled (setting, or
  `FIREWALLA_ENABLE_WRITES=true` for the CLI) **and** the call carries `confirm: true`
  (`--confirm` on the CLI, `"confirm": true` in an MCP call or HTTP `POST` body).
- SSH operations are read-only. Changing networks, VLANs, DHCP scopes or reservations over SSH
  is planned, not built, and will stay off by default behind **Enable SSH writes**.
- The MSP token is never logged or returned in errors.

## SSH access is unofficial

Firewalla documents only the MSP API. Everything done over SSH reads Firewalla's internal files
and commands, which can change with any firmware release:

- The DHCP lease file is assumed to be `/home/pi/.router/run/dhcp/dnsmasq.leases`, with
  `/var/lib/misc/dnsmasq.leases` as a fallback. **Unverified** until checked on a real box.
- Firmware version is read from the git checkout at `/home/pi/firewalla`. **Unverified.**
- Network, VLAN and DHCP writes need an on-box spike first: map where the configuration lives
  (Redis and `~/.firewalla/config`), how a change is applied, and which firmware versions
  behave the same. Writes will refuse to run on an unverified firmware version.

## Known gaps

- **MCP tools may not appear yet.** The SDK (v2.0.0) names the point `mcp.tool`; the gateway
  registers tool providers under `host.mcp.tool`. The plugin declares `mcp.tool`, because the SDK
  refuses to start a plugin that declares `host.mcp.tool`. HTTP and CLI work regardless.
- **The MSP token setting is not readable by the plugin yet.** The gateway keeps secret settings
  private and offers plugins no way to read them. Until it does, the plugin process reads the
  token from `FIREWALLA_MSP_TOKEN` in its environment or from `msp-token` (mode 0600) in its
  state directory.
- The plugin's public key is available through `firewalla_ssh_pubkey` and
  `GET /p/firewalla/api/ssh/pubkey`, not in the settings page, because settings values are owned
  by the gateway.

## Development

```bash
go test ./...                                        # from this folder
bash scripts/ci/plugin-build-v1.sh "$(mktemp -d)"    # build hook, as the pipeline runs it
```
