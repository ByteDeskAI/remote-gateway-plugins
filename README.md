# ByteDesk remote-gateway plugins

Every ByteDesk remote-gateway plugin that is not part of the gateway kernel lives here, one
folder per plugin under `plugins/`. Any plugin that follows the [plugin contract](docs/CONTRACT.md)
is tested, versioned, changelogged, built and published to the ByteDesk Store by the same pipeline.
No per-plugin CI setup is needed.

| Branch | What happens | Store |
|---|---|---|
| Pull request | Tests and contract checks only | none |
| `develop` | Each changed plugin is built as `<next>-dev.<build>` | store.dev.bytedesk.ai |
| `main` | Each changed plugin is released as `X.Y.Z`, its CHANGELOG is written, and it is tagged `<id>/vX.Y.Z` | store.bytedesk.ai |

## Layout
- `plugins/<id>/`: one plugin, in any language (see [docs/CONTRACT.md](docs/CONTRACT.md)).
- `plugins/_template/`: the starting point for a new plugin (`tools/new-plugin <id>`).
- `kit/`: shared Go module. One table of operations generates a plugin's MCP tools, HTTP routes, CLI and settings.
- `tools/release/`: works out which plugins changed and their next version, writes changelogs and tags.
- `tools/build/`: the shared build runner that validates and packs a plugin with the pinned SDK.
- `.teamcity/`: the pipeline (TeamCity versioned settings).

## Contributing
See [CONTRIBUTING.md](CONTRIBUTING.md). In short: branch from `develop`, use conventional commits
scoped to the plugin (`feat(firewalla): …`), and open a pull request to `develop`.

Licensed under the [Apache License 2.0](LICENSE).
