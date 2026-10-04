# TeamCity pipeline: operator runbook

This folder is the whole build and release pipeline for the plugins in this repository. TeamCity
(`deploy.prod.bytedesk.ai`) reads it as versioned settings. There are no GitHub Actions.
Adding a plugin never changes anything here.

Before the first run, an operator must do three things, in this order:

1. [Create the three credentials](#credentials) and put their ids in `Shared.kt`.
2. [Connect the project and place it under `ByteDesk`](#place-under-bytedesk), with someone watching.
3. [Set up GitHub](#github-settings): branch protection, the release bot's bypass, and the required check.

## What each build does

Dev and prod are fixed by branch. There is no switch to set and nothing to choose per run.

| Build | Runs when | What it does | Credentials it holds |
|---|---|---|---|
| **Check** | A pull request to `develop` or `main` is opened or updated | `release check` (contract), `release check --commits BASE..HEAD` (commit scopes), then builds every changed plugin with `tools/build` | None |
| **Build dev** | A push to `develop` | `release plan --mode dev --build <build number>`, then builds each planned plugin as `<next>-dev.<build>`. No commits, tags or pushes | Read-only GitHub token for private Go modules (`%github.token%`) |
| **Release main** | A push to `main` | Plans, writes `VERSION` and `CHANGELOG.md`, makes one release commit and tags `<id>/vX.Y.Z` locally, builds every plugin from that commit, then pushes `main` and the tags atomically and merges `main` into `develop` | Release-bot GitHub token, plus the read-only module token |
| **Publish dev** | Build dev succeeds | Takes Build dev's `dist/` unchanged and runs `tools/publish/publish.sh --store https://store.dev.bytedesk.ai` | Store **dev** admin token, and nothing else |
| **Publish prod** | Release main succeeds | Same, with `--store https://store.bytedesk.ai` | Store **prod** admin token, and nothing else |

Notes on behaviour:

- **Nothing to release is a success.** Build dev and Release main finish green with an empty
  `dist/`. Publish then has nothing to upload and also finishes green.
- **The release bot's own push re-triggers Release main and Build dev.** Each finds nothing to
  release and stops. This is expected.
- **Release main pushes only after every plugin built.** If a build fails, GitHub is untouched.
  If the push is rejected because `main` moved during the build, re-run the build.
- **If merging `main` into `develop` conflicts,** the release is already pushed and tagged, and the
  build fails with a message. A maintainer merges `main` into `develop` by hand.
- **Publish is safe to re-run.** Uploading the same bytes again returns HTTP 200 and passes. A
  version that already holds *different* bytes returns HTTP 409 and fails the build; Store versions
  are immutable and the pipeline never uses `?force=1`.
- **Every credential name is cleared at project level** (the same list as the gateway's
  `Plugins.kt`). Each secret is set again only on the one build that needs it.

## Credentials

Create these three secure values in TeamCity, then replace the placeholder ids in `Shared.kt`.
Until you do, Release main and the two Publish builds cannot start.

| Constant in `Shared.kt` | Used by | Value | Second copy in Infisical (Infrastructure project) |
|---|---|---|---|
| `STORE_DEV_ADMIN_TOKEN` | Publish dev, as `env.STORE_ADMIN_TOKEN` | A `STORE_ADMIN_TOKEN` accepted by store.dev.bytedesk.ai | `prod` env, `/teamcity/STORE_DEV_ADMIN_TOKEN` |
| `STORE_PROD_ADMIN_TOKEN` | Publish prod, as `env.STORE_ADMIN_TOKEN` | A `STORE_ADMIN_TOKEN` accepted by store.bytedesk.ai | `prod` env, `/teamcity/STORE_PROD_ADMIN_TOKEN` |
| `RELEASE_BOT_GITHUB_TOKEN` | Release main, as `env.RELEASE_GIT_TOKEN` | A GitHub token of the release-bot account with `contents: write` on this repository only | `prod` env, `/teamcity/RELEASE_BOT_GITHUB_TOKEN` |

How to create one: open the project in TeamCity, go to **Versioned Settings > Tokens**
(or **Parameters > Add new parameter**, type *Password*), enter the value, and copy the generated
`credentialsJSON:<uuid>` id into `Shared.kt`. Record each Infisical path, by name only, in
`ByteDeskAI/infrastructure/registry`.

Things to know about the Store tokens:

- **store.dev.bytedesk.ai does not accept a token today.** Its deployment
  (`bytedesk-store/deploy/k8s-dev.yaml`) sets only `STORE_ADMIN_PASSWORD`, from Kubernetes
  secret `store-admin`. The Store accepts `Authorization: Bearer <STORE_ADMIN_TOKEN>` only when the
  server has `STORE_ADMIN_TOKEN` set (`internal/httpapi/admin_session.go`). Add a
  `STORE_ADMIN_TOKEN` key to that deployment first, from a new random value, and store the same
  value in TeamCity and Infisical.
- **store.bytedesk.ai does not exist yet** (no DNS record on 2026-10-04). Publish prod fails until
  the production Store is deployed with its own `STORE_ADMIN_TOKEN`.
- Move both to a publisher-scoped credential (ADR 0032) when the Store supports one.

Also set `env.RELEASE_GIT_NAME` and `env.RELEASE_GIT_EMAIL` on Release main (in `settings.kts`)
to the release bot's GitHub login and its noreply email, so release commits are attributed to it.

Build dev and Release main read `%github.token%` for private ByteDeskAI Go modules. That parameter
is inherited from the `ByteDesk` project. If the project is not yet under `ByteDesk`, those two
builds wait with "undefined parameter" until it is.

## Place under ByteDesk

The org rule is that this project is a child of the `ByteDesk` project (declared in
`bytedesk-platform/.teamcity/settings.kts`), so it inherits shared parameters.

**This cannot be done in the DSL.** TeamCity 2026.1 rejects `parentId` on a versioned-settings root:
"'parentId' property cannot be changed in the root of a relative project hierarchy" (found by
compiling this DSL locally). The settings here are relative, so moving the project does not change
them. Do it once, by hand, with someone watching the TeamCity UI:

1. Under `ByteDesk`, create project **RemoteGatewayPlugins** from the repository URL
   `https://github.com/ByteDeskAI/remote-gateway-plugins.git` (anonymous; the repository is public).
   If it was already created elsewhere, use **Project settings > Actions > Move project** to move
   it under `ByteDesk` instead. Do not recreate it; that loses build history.
2. Turn on **Versioned Settings**: synchronization enabled, settings format Kotlin, VCS root this
   repository, branch `main`, "use settings from VCS".
3. Confirm the import shows five builds (Check, Build dev, Release main, Publish dev,
   Publish prod), one VCS root, and no errors. Missing-token warnings are expected until the
   [credentials](#credentials) exist.
4. Confirm the project's parent is `ByteDesk` and that `%github.token%` resolves on Build dev
   (**Parameters** tab).
5. Before the first run, assign **Check** to an agent pool that holds no credentials and no
   cached secrets, because Check runs pull-request code.

## GitHub settings

Owner: a GitHub organization admin.

- **Branch protection on `main` and `develop`:** require a pull request with one review, and
  require the status check that TeamCity publishes for **Check**.
- **Release bot bypass on `main` and `develop`:** allow only the release-bot account to push
  directly (ruleset bypass list, or "Allow specified actors to bypass required pull requests").
  It needs this for the release commit, the `<id>/v*` tags, and the back-merge into `develop`.
  Give it no other bypass and no admin role.
- **Tag protection:** restrict creating `*/v*` tags to the release bot.
- **Fork pull requests:** Check builds automatically only for members and collaborators
  (`filterAuthorRole = MEMBER_OR_COLLABORATOR`). For anyone else, a maintainer reads the diff,
  then runs Check by hand on that pull request's branch. That manual run is the approval.

## Local checks

```bash
bash tools/publish/publish_test.sh          # publish.sh against a fake Store
mvn -f .teamcity/pom.xml teamcity-configs:generate   # DSL compiles (needs JDK 21)
```

The DSL is split in two files because objects in `settings.kts` may not capture script-level
values: `Shared.kt` holds the credential ids, the VCS root and the Publish build shape.
Keep all comments as `//` line comments.
