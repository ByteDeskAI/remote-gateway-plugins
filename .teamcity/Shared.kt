import jetbrains.buildServer.configs.kotlin.*
import jetbrains.buildServer.configs.kotlin.buildSteps.script
import jetbrains.buildServer.configs.kotlin.triggers.finishBuildTrigger
import jetbrains.buildServer.configs.kotlin.vcs.GitVcsRoot

// Shared constants and the Publish build shape, kept out of settings.kts:
// objects declared in a .kts script may not capture script-level values.

// Server-side GitHub PAT already used across ByteDeskAI repos (pull-request
// detection and commit statuses only; it never reaches a build).
const val GITHUB_PAT = "credentialsJSON:9f982ee0-ced1-48d1-97c3-335b95430645"

// TODO(operator): these three secure tokens do not exist yet. Create each in
// TeamCity (project RemoteGatewayPlugins > Versioned Settings > Tokens, or
// Parameters > Add > Password and copy the generated credentialsJSON id), then
// replace the placeholder id here. Until then the Release and Publish builds
// cannot start. See .teamcity/README.md, "Credentials".
const val STORE_DEV_ADMIN_TOKEN = "credentialsJSON:00000000-0000-0000-0000-000000000001"
const val STORE_PROD_ADMIN_TOKEN = "credentialsJSON:00000000-0000-0000-0000-000000000002"
const val RELEASE_BOT_GITHUB_TOKEN = "credentialsJSON:00000000-0000-0000-0000-000000000003"

// Public repository: anonymous fetch, so no credential is handed to agents
// during checkout, including on fork pull requests.
object PluginsVcs : GitVcsRoot({
    id("Vcs")
    name = "remote-gateway-plugins"
    url = "https://github.com/ByteDeskAI/remote-gateway-plugins.git"
    branch = "refs/heads/develop"
    branchSpec = """
        +:refs/heads/(develop)
        +:refs/heads/(main)
    """.trimIndent()
})

// Shared shape of the two Publish builds: run after the source build succeeds,
// take its dist/ unchanged, upload with tools/publish/publish.sh.
fun publish(bt: BuildType, source: BuildType, branch: String, storeUrl: String, token: String) = bt.apply {
    maxRunningBuilds = 1
    vcs {
        root(PluginsVcs)
        checkoutMode = CheckoutMode.ON_AGENT
        cleanCheckout = true
        branchFilter = branch
    }
    params {
        param("env.STORE_URL", storeUrl)
        password("env.STORE_ADMIN_TOKEN", token, display = ParameterDisplay.HIDDEN)
    }
    steps {
        script {
            name = "publish"
            scriptContent = "bash tools/publish/publish.sh --store \"%env.STORE_URL%\" --dist dist"
        }
    }
    triggers {
        finishBuildTrigger {
            buildType = "${source.id}"
            successfulOnly = true
            branchFilter = branch
        }
    }
    dependencies {
        snapshot(source) {
            reuseBuilds = ReuseBuilds.SUCCESSFUL
            onDependencyFailure = FailureAction.FAIL_TO_START
            onDependencyCancel = FailureAction.FAIL_TO_START
        }
        artifacts(source) {
            buildRule = sameChainOrLastFinished()
            cleanDestination = true
            // Optional: a build with nothing to release has no dist/.
            artifactRules = "?:dist/** => dist"
        }
    }
    requirements {
        contains("teamcity.agent.jvm.os.name", "Linux")
    }
}
