# Contributing

1. Branch from `develop`: `feature/<id>-<topic>`.
2. Use [Conventional Commits](https://www.conventionalcommits.org/), scoped to the plugin folder
   you change: `feat(firewalla): add rule pause tool`, `fix(tmux-manager): …`. A commit that touches
   more than one plugin needs one scope per plugin, or separate commits.
   - `feat` → minor, `fix`/`perf` → patch, `!` or a `BREAKING CHANGE:` footer → major.
   - `chore`, `docs`, `test`, `refactor`, `ci` do not release anything.
3. Do not edit `VERSION`, or the version in `plugin.json`, by hand. Add changelog notes only in the
   `## [Unreleased]` section, if at all; the release writes the rest from your commits.
4. Open a pull request to `develop`. The Check build must pass and a ByteDesk maintainer must review it.
   Pull requests from forks run only after a maintainer approves the build.

Releases go `develop` → `release/*` → `main`, as in gitflow. Only maintainers merge to `main`.

## Adding a plugin
Run `tools/new-plugin <id>`, then follow [docs/CONTRACT.md](docs/CONTRACT.md). Nothing in `.teamcity/` changes.
