import jetbrains.buildServer.configs.kotlin.*
import jetbrains.buildServer.configs.kotlin.buildFeatures.PullRequests
import jetbrains.buildServer.configs.kotlin.buildFeatures.commitStatusPublisher
import jetbrains.buildServer.configs.kotlin.buildFeatures.pullRequests
import jetbrains.buildServer.configs.kotlin.buildSteps.script
import jetbrains.buildServer.configs.kotlin.triggers.VcsTrigger
import jetbrains.buildServer.configs.kotlin.triggers.vcs

// ByteDesk remote-gateway plugins: TeamCity versioned settings (Kotlin DSL).
//
// Five builds, one per pipeline stage. Dev and prod are fixed by branch:
//   Check          pull requests            no credentials
//   Build dev      develop                  no write credentials
//   Release main   main                     release-bot GitHub token
//   Publish dev    after Build dev          store.dev.bytedesk.ai admin token
//   Publish prod   after Release main       store.bytedesk.ai admin token
// Shell logic lives in .teamcity/scripts/ and tools/, so the DSL stays free of
// Kotlin escaping. See .teamcity/README.md for the operator runbook.
//
// Keep all comments as // line comments. A nested block comment silently
// swallows the rest of the project (same footgun as bytedesk-platform).

version = "2026.1"

project {
    // Intended parent: the ByteDesk project declared in bytedesk-platform's
    // .teamcity/settings.kts (org rule). It cannot be set here: the TeamCity
    // 2026.1 DSL validator rejects parentId on a versioned-settings root
    // ("'parentId' property cannot be changed in the root of a relative project
    // hierarchy"). An operator moves the project once in the TeamCity UI; this
    // DSL is relative, so it is unaffected. See README.md, "Place under ByteDesk".
    description = "ByteDesk remote-gateway plugins monorepo: check, build, release and Store publish"

    vcsRoot(PluginsVcs)

    buildType(Check)
    buildType(BuildDev)
    buildType(ReleaseMain)
    buildType(PublishDev)
    buildType(PublishProd)

    // Clear every credential name any ByteDesk parent might define, as
    // bytedesk-remote-gateway/.teamcity/Plugins.kt does. Only the one build that
    // needs a secret overrides its own name. Defense in depth, not isolation:
    // agents still need the isolated pool described in README.md.
    params {
        param("env.STORE_ADMIN_TOKEN", "")
        param("env.STORE_ADMIN_PASSWORD", "")
        param("env.BYTEDESK_STORE_ADMIN_TOKEN", "")
        param("env.RELEASE_GIT_TOKEN", "")
        param("env.R2_ACCESS_KEY_ID", "")
        param("env.R2_SECRET_ACCESS_KEY", "")
        param("env.AWS_ACCESS_KEY_ID", "")
        param("env.AWS_SECRET_ACCESS_KEY", "")
        param("env.AWS_SESSION_TOKEN", "")
        param("env.NPM_TOKEN", "")
        param("env.NODE_AUTH_TOKEN", "")
        param("env.GITHUB_TOKEN", "")
        param("env.BYTEDESK_GIT_TOKEN", "")
        param("env.GOFLAGS", "-mod=readonly")
    }
}

object Check : BuildType({
    id("Check")
    name = "Check"
    description = "Pull requests: contract, commit scopes, and a build of each changed plugin. No credentials."

    vcs {
        root(PluginsVcs)
        checkoutMode = CheckoutMode.ON_AGENT
        cleanCheckout = true
    }
    params {
        // Untrusted code: no Go module token either. Private ByteDeskAI modules
        // must come from the public SDK or fail here (see README.md).
        param("env.BYTEDESK_GIT_TOKEN", "")
        param("env.PR_TARGET_BRANCH", "%teamcity.pullRequest.target.branch%")
    }
    steps {
        script {
            name = "check"
            scriptContent = "bash .teamcity/scripts/check.sh"
        }
    }
    triggers {
        vcs {
            // Only pull-request branches added by the feature below.
            branchFilter = "+pr: target=develop\n+pr: target=main"
            quietPeriodMode = VcsTrigger.QuietPeriodMode.USE_CUSTOM
            quietPeriod = 30
            enableQueueOptimization = true
        }
    }
    features {
        pullRequests {
            vcsRootExtId = "${PluginsVcs.id}"
            provider = github {
                authType = token { token = GITHUB_PAT }
                filterTargetBranch = "+:refs/heads/develop\n+:refs/heads/main"
                // Fork PRs from non-members are not built automatically. A
                // maintainer reviews the diff, then runs Check on that PR branch
                // by hand: that run is the approval (README.md).
                filterAuthorRole = PullRequests.GitHubRoleFilter.MEMBER_OR_COLLABORATOR
            }
        }
        commitStatusPublisher {
            vcsRootExtId = "${PluginsVcs.id}"
            publisher = github {
                githubUrl = "https://api.github.com"
                authType = personalToken { token = GITHUB_PAT }
            }
        }
    }
    artifactRules = "?:dist/** => dist"
    requirements {
        contains("teamcity.agent.jvm.os.name", "Linux")
        equals("env.BYTEDESK_AGENT_ARCH", "amd64")
    }
})

object BuildDev : BuildType({
    id("BuildDev")
    name = "Build dev"
    description = "develop: each changed plugin as <next>-dev.<build>; no git writes"
    maxRunningBuilds = 1

    vcs {
        root(PluginsVcs)
        checkoutMode = CheckoutMode.ON_AGENT
        cleanCheckout = true
        branchFilter = "+:develop"
    }
    params {
        // Read access to private ByteDeskAI Go modules, inherited from ByteDesk.
        param("env.BYTEDESK_GIT_TOKEN", "%github.token%")
    }
    steps {
        script {
            name = "plan and build"
            scriptContent = "bash .teamcity/scripts/build-dev.sh"
        }
    }
    triggers {
        vcs {
            branchFilter = "+:develop"
            quietPeriodMode = VcsTrigger.QuietPeriodMode.USE_CUSTOM
            quietPeriod = 60
            enableQueueOptimization = true
        }
    }
    artifactRules = "?:dist/** => dist"
    requirements {
        contains("teamcity.agent.jvm.os.name", "Linux")
        equals("env.BYTEDESK_AGENT_ARCH", "amd64")
    }
})

object ReleaseMain : BuildType({
    id("ReleaseMain")
    name = "Release main"
    description = "main: version, changelog, tag <id>/vX.Y.Z, build, push, back-merge to develop"
    maxRunningBuilds = 1

    vcs {
        root(PluginsVcs)
        checkoutMode = CheckoutMode.ON_AGENT
        cleanCheckout = true
        branchFilter = "+:main"
    }
    params {
        param("env.BYTEDESK_GIT_TOKEN", "%github.token%")
        password("env.RELEASE_GIT_TOKEN", RELEASE_BOT_GITHUB_TOKEN, display = ParameterDisplay.HIDDEN)
        // TODO(operator): set to the release bot's GitHub login and noreply email.
        param("env.RELEASE_GIT_NAME", "bytedesk-release-bot")
        param("env.RELEASE_GIT_EMAIL", "release-bot@bytedesk.ai")
    }
    steps {
        script {
            name = "release"
            scriptContent = "bash .teamcity/scripts/release-main.sh"
        }
    }
    triggers {
        vcs {
            // The bot's own release commit re-triggers this build; the plan is
            // then empty and the build is a no-op.
            branchFilter = "+:main"
            quietPeriodMode = VcsTrigger.QuietPeriodMode.USE_CUSTOM
            quietPeriod = 60
            enableQueueOptimization = true
        }
    }
    artifactRules = "?:dist/** => dist"
    requirements {
        contains("teamcity.agent.jvm.os.name", "Linux")
        equals("env.BYTEDESK_AGENT_ARCH", "amd64")
    }
})

object PublishDev : BuildType({
    id("PublishDev")
    name = "Publish dev"
    description = "Uploads Build dev packages to store.dev.bytedesk.ai; the only build with its admin token"
    publish(this, BuildDev, "+:develop", "https://store.dev.bytedesk.ai", STORE_DEV_ADMIN_TOKEN)
})

object PublishProd : BuildType({
    id("PublishProd")
    name = "Publish prod"
    description = "Uploads Release main packages to store.bytedesk.ai; the only build with its admin token"
    publish(this, ReleaseMain, "+:main", "https://store.bytedesk.ai", STORE_PROD_ADMIN_TOKEN)
})
