"""Build, validate and pack one plugin of this repository; never publishes or signs.

Vendored from bytedesk-remote-gateway/.teamcity/scripts/plugin-build-v1.py
(plugin build contract v1). The one change: the plugin is a folder,
--plugin-dir plugins/<id>, inside this repository's clean Git checkout, so the
revision check is on the repository and the hook runs from the plugin folder.
After the hook, the version is stamped into the staged plugin.json with
`release stamp` (tools/release), then the pinned SDK validates and packs it.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import struct
import subprocess
import sys
import tarfile
import tempfile


SDK_REPO = 'github.com/ByteDeskAI/bytedesk-remote-gateway-plugin-sdk'
# One SDK pin for the whole repository (bytedesk-jev's pin when this was written;
# contract-2 manifests need the v2 module's plugin-sdk). Renovate bumps it.
DEFAULT_SDK_VERSION = 'v2.0.0'
SEMVER = r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?'


def require(ok, message):
    if not ok:
        raise ValueError(message)


def run(args, cwd, env):
    # Dependency errors can echo credential-bearing Git rewrites. Do not relay
    # subprocess output or environments into the shared build log.
    result = subprocess.run(args, cwd=cwd, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    require(result.returncode == 0, Path(args[0]).name + ' step failed (exit ' + str(result.returncode) + '; subprocess output suppressed)')


def checkout(path, env):
    def git(*args):
        return subprocess.check_output(['git', '-C', str(path), *args], env=env, text=True, stderr=subprocess.DEVNULL).strip()
    require(Path(git('rev-parse', '--show-toplevel')).resolve() == path.resolve(),
            'expected an exact Git checkout root: ' + str(path))
    revision = git('rev-parse', 'HEAD')
    require(re.fullmatch(r'[0-9a-f]{40}', revision), 'checkout requires full SHA-1 revision')
    require(not git('status', '--porcelain', '--untracked-files=all'), 'checkout is dirty: ' + str(path))
    return revision


def safe_tree(root):
    for directory, dirs, files in os.walk(root, followlinks=False):
        for name in dirs + files:
            path = Path(directory) / name
            mode = path.lstat().st_mode
            require(not stat.S_ISLNK(mode), 'staging contains symlink: ' + str(path.relative_to(root)))
            require(stat.S_ISREG(mode) or stat.S_ISDIR(mode), 'staging contains special file')
            require(name not in ('.git', '.env', 'control.env', 'SHA256SUMS', '.unsigned'), 'staging contains forbidden source/credential or reserved SDK file')
            require(not any(ord(char) < 32 or ord(char) == 127 for char in name), 'staging filename contains controls')


def validate_binary(binary, platform):
    require(binary.is_file() and os.access(binary, os.X_OK), 'manifest binary missing or not executable')
    with binary.open('rb') as f:
        header = f.read(20)
    require(len(header) == 20 and header[:4] == b'\x7fELF' and header[4:6] == b'\x02\x01', 'expected little-endian 64-bit ELF executable')
    machine = struct.unpack('<H', header[18:20])[0]
    require(machine == {'linux-amd64': 62, 'linux-arm64': 183}[platform], 'binary platform mismatch')


def sdk_module(sdk_version):
    # Go's semantic import versioning: major 2+ lives under /vN.
    major = int(sdk_version[1:].split('.')[0])
    return SDK_REPO + ('/v' + str(major) if major >= 2 else '') + '/cmd/plugin-sdk'


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    for flag in ('plugin-dir', 'revision', 'id', 'version', 'out'):
        parser.add_argument('--' + flag, required=True)
    parser.add_argument('--sdk-version', default=DEFAULT_SDK_VERSION)
    parser.add_argument('--platform', required=True, choices=('linux-amd64', 'linux-arm64'))
    args = parser.parse_args()
    require(re.fullmatch(r'[0-9a-f]{40}', args.revision), 'revision must be full lowercase SHA-1')
    require(re.fullmatch(r'[a-z0-9][a-z0-9_-]{0,63}', args.id), 'invalid plugin id')
    require(re.fullmatch(SEMVER, args.version), 'version must be explicit SemVer')
    require(re.fullmatch('v' + SEMVER, args.sdk_version), 'SDK version must be pinned SemVer')
    # The runner lives in the repository it builds: tools/build/ under the root.
    repo = Path(__file__).resolve().parents[2]
    out = Path(args.out).resolve()
    require(not Path(args.plugin_dir).is_absolute() and '..' not in Path(args.plugin_dir).parts,
            '--plugin-dir must be plugins/<name>, relative to the repository root')
    unresolved = repo / args.plugin_dir
    source = unresolved.resolve()
    require(not unresolved.is_symlink() and source.parent == repo / 'plugins' and source.is_dir(),
            '--plugin-dir must be an existing folder plugins/<name>')
    # A plugin folder is named after its id; "_" folders (the template) are not plugins.
    require(source.name == args.id or source.name.startswith('_'), 'plugin folder name must equal --id')
    require(not out.exists(), 'output must not exist')
    require(not out.is_relative_to(repo) and not repo.is_relative_to(out), 'output must be outside the repository checkout')
    env = dict(os.environ)
    forbidden = [key for key in env if (('STORE' in key.upper() and any(x in key.upper() for x in ('TOKEN', 'SECRET', 'PASSWORD', 'KEY'))) or key.upper() in ('COMMERCIAL_SIGNING_KEY', 'STORE_SIGNING_SEED_FILE')) and env[key]]
    require(not forbidden, 'publication/signing credentials must not be present in build environment')
    # Never resolve a local workspace override instead of the released SDK.
    env['GOWORK'] = 'off'
    require(checkout(repo, env) == args.revision, 'source revision mismatch')
    hook = source / 'scripts/ci/plugin-build-v1.sh'
    require(hook.is_file() and not hook.is_symlink(), 'fixed build hook missing or symlinked')
    require(hook.resolve().is_relative_to(source), 'build hook escapes checkout')
    with tempfile.TemporaryDirectory(prefix='bytedesk-plugin-build-') as tmp:
        scratch = Path(tmp)
        stage, packed, bin_dir = scratch / 'stage', scratch / 'packed', scratch / 'bin'
        stage.mkdir()
        bin_dir.mkdir()
        env['GOBIN'] = str(bin_dir)
        env['PLUGIN_BUILD_PLATFORM'] = args.platform
        # Hook owns language/toolchain-specific tests and compilation only.
        run(['bash', str(hook), str(stage)], source, env)
        require(checkout(repo, env) == args.revision, 'hook changed the repository')
        require(stage.is_dir() and not stage.is_symlink(), 'hook replaced staging directory')
        safe_tree(stage)
        # Stamp the build's version into the staged manifest only (never the repo).
        run(['go', 'build', '-o', str(bin_dir / 'release'), '.'], repo / 'tools/release', env)
        run([str(bin_dir / 'release'), 'stamp', '--id', args.id, '--version', args.version, '--dir', str(stage)], scratch, env)
        manifest = json.loads((stage / 'plugin.json').read_text())
        require(manifest.get('id') == args.id and manifest.get('version') == args.version, 'manifest identity/version mismatch')
        # Contract 1 marks a process plugin with "spawn": true, contract 2 with "kind": "process".
        require(manifest.get('spawn') is True or manifest.get('kind') == 'process', 'only spawned Gateway process plugins are supported')
        binary = manifest.get('binary', '')
        require(isinstance(binary, str) and binary and '/' not in binary and '\\' not in binary and '..' not in binary, 'invalid binary basename')
        validate_binary(stage / binary, args.platform)
        (bin_dir / 'release').unlink()
        run(['go', 'install', sdk_module(args.sdk_version) + '@' + args.sdk_version], scratch, env)
        sdk = str(bin_dir / 'plugin-sdk')
        run([sdk, 'validate', '--dir', str(stage)], scratch, env)
        run([sdk, 'pack', '--dir', str(stage), '--out', str(packed)], scratch, env)
        archive = packed / (args.id + '-' + args.version + '.tar.gz')
        require(archive.is_file() and not archive.is_symlink(), 'SDK archive missing')
        require(archive.stat().st_size <= 128 * 1024 * 1024, 'archive exceeds Store upload limit')
        with tarfile.open(archive, 'r:gz') as tar:
            seen = set()
            hashes = {}
            checksums = None
            expanded_size = 0
            for member in tar:
                parts = member.name.split('/')
                require(parts[0] == args.id and '..' not in parts and not member.name.startswith('/'), 'unsafe archive path')
                require(member.isfile() or member.isdir(), 'unsafe archive member type')
                require(member.name not in seen, 'duplicate archive member')
                seen.add(member.name)
                if member.isfile():
                    expanded_size += member.size
                    require(expanded_size <= 512 * 1024 * 1024, 'expanded archive exceeds v1 limit')
                    stream = tar.extractfile(member)
                    if member.name == args.id + '/SHA256SUMS':
                        require(member.size <= 1024 * 1024, 'member checksum list too large')
                        checksums = stream.read().decode('utf-8')
                    elif member.name != args.id + '/.unsigned':
                        digest_state = hashlib.sha256()
                        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                            digest_state.update(chunk)
                        hashes[member.name] = digest_state.hexdigest()
            require(args.id + '/plugin.json' in seen and args.id + '/' + binary in seen, 'archive missing manifest or executable')
            require(checksums is not None and args.id + '/.unsigned' in seen, 'archive missing SDK checksums or unsigned marker')
            expected_hashes = {}
            for line in checksums.splitlines():
                digest_value, separator, name = line.partition('  ')
                require(separator and re.fullmatch('[0-9a-f]{64}', digest_value) and name not in expected_hashes, 'invalid member checksum list')
                expected_hashes[name] = digest_value
            require(expected_hashes == hashes, 'archive member checksums mismatch')
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        provenance = {'contractVersion': 1, 'id': args.id, 'version': args.version,
                      'platform': args.platform, 'sourceRevision': args.revision,
                      'pluginDir': source.relative_to(repo).as_posix(),
                      'runnerRevision': args.revision, 'sdkModule': sdk_module(args.sdk_version),
                      'sdkVersion': args.sdk_version, 'archive': archive.name, 'sha256': digest}
        # Publish local CI outputs only after all checks; never contact Store.
        out.mkdir(parents=True, exist_ok=False)
        (out / archive.name).write_bytes(archive.read_bytes())
        (out / 'SHA256SUMS').write_text(digest + '  ' + archive.name + '\n')
        (out / 'provenance.json').write_text(json.dumps(provenance, indent=2) + '\n')
        print('plugin-build PASS: ' + archive.name + ' ' + digest)


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError, tarfile.TarError) as error:
        print('plugin-build: ' + str(error), file=sys.stderr)
        sys.exit(1)
