# Plugin contract

A plugin that meets this contract is built, versioned, changelogged and published by the shared
pipeline. Nothing else is needed: no TeamCity change and no registration. `tools/release check`
enforces every rule below on every pull request.

## A plugin is a folder `plugins/<id>/` containing

| File | Rule |
|---|---|
| `plugin.json` | Gateway manifest. `id` equals the folder name. `version` equals `VERSION`. Contract 2 for new plugins. |
| `VERSION` | One line, SemVer `X.Y.Z`, no prefix. Written only by the release. |
| `CHANGELOG.md` | [Keep a Changelog](https://keepachangelog.com/) with a `## [Unreleased]` section. Release sections are written by the release. |
| `scripts/ci/plugin-build-v1.sh` | Executable build hook (see below). |

Folders starting with `_` (for example `_template`) are not plugins and are never released.
`<id>` is lowercase kebab-case, 2–40 characters.

## Build hook
`scripts/ci/plugin-build-v1.sh <stage>` is called from the plugin folder with one argument: an
absolute, empty staging directory outside the repository. `PLUGIN_BUILD_PLATFORM`
(`linux-amd64` or `linux-arm64`) names the target. The hook must:

1. Run the plugin's tests and fail if any fail.
2. Build with its own pinned toolchain and lockfile (`go.mod`/`go.sum`, `Cargo.lock`, …).
3. Copy only `plugin.json`, the executable named by `plugin.json` `binary`, and required runtime
   assets into the staging directory.
4. Leave the repository unchanged.

The shared runner (`tools/build/`) then stamps the release version into the staged `plugin.json`,
validates it, and packs it with the pinned gateway SDK, producing `<id>-<version>.tar.gz`,
`SHA256SUMS` and `provenance.json`. This is the gateway's
[plugin build contract v1](https://github.com/ByteDeskAI/bytedesk-remote-gateway/blob/main/docs/plugins/PLUGIN_BUILD_CONTRACT.md),
adapted to plugins that live in a sub-folder of one repository.

## Versions
- Each plugin is versioned on its own. Its released versions are the tags `<id>/vX.Y.Z`.
- The next version comes from the conventional commits since the last tag that touch `plugins/<id>/`
  (see CONTRIBUTING.md). No releasable commits means no release.
- `develop` builds publish `<next>-dev.<build>` to store.dev.bytedesk.ai. `main` releases publish
  `X.Y.Z` to store.bytedesk.ai.
- Store versions are immutable. The pipeline never re-uploads different bytes under a version.

## Secrets
Never commit credentials, and never copy them into staging. A plugin that needs a user's
credentials declares `secret` fields in its settings section, so the gateway host stores them.
Build hooks run untrusted pull request code and get no credentials beyond read access to
ByteDesk Go modules.
