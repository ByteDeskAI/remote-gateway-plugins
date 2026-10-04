# Plugin template

The starting point for a new plugin. Do not edit it to build a plugin; run
`tools/new-plugin <id>` from the repository root, which copies this folder to
`plugins/<id>/` and renames the plugin id, Go module and binary.

This folder starts with `_`, so the release tools never version or publish it.
See [docs/CONTRACT.md](../../docs/CONTRACT.md) for what a plugin must contain.
