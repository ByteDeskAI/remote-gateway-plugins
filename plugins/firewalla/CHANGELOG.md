# Changelog

All notable changes to the Firewalla plugin are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Release sections are written by the release pipeline.

## [Unreleased]

### Added
- One binary with two modes: `firewalla serve` runs as a ByteDesk gateway plugin, and
  `firewalla <noun> <verb>` is a CLI that reads `FIREWALLA_*` environment variables.
- Firewalla MSP API v2 operations: boxes list; devices list and rename; rules list, create,
  pause, resume and delete; alarms list, get, archive, mute and delete; flows list with cursor
  paging; simple and top statistics; target lists list, get, create, update and delete.
- Read-only SSH operations on the box as user `pi`: interfaces, routes, DHCP leases and
  firmware version. Host keys are trusted on first use and verified afterwards.
- `firewalla ssh bootstrap`: installs an SSH key for `pi` using the one-time password from the
  Firewalla app, plus a `post_main.d` hook that restores the key after reboots, and prints the
  rollback command.
- The same operations as MCP tools (`firewalla_*`), HTTP routes (`/p/firewalla/api/<noun>/<verb>`)
  and CLI commands, generated from one operation table.
- Safety: write and destructive operations are off by default, need the "Enable write tools"
  setting, and need `confirm: true` (or `--confirm`) on every call.
- Gateway settings section with the MSP domain, MSP API token (secret), box address and the
  write toggles. The plugin's SSH key pair is generated in its state directory with mode 0600.
