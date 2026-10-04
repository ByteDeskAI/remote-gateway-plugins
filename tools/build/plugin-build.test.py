"""Tests for plugin-build.py, adapted from the gateway's plugin-build-v1.test.py."""
import json
import os
import shutil
from pathlib import Path
import subprocess
import tempfile
import unittest


HERE = Path(__file__).resolve().parent
SHA = 'a' * 40


class BuildContractTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        # A throwaway copy of the repository layout: tools/build (this runner),
        # tools/release (built by the fake go) and plugins/example.
        self.repo = self.root / 'repo'
        shutil.copytree(HERE, self.repo / 'tools/build', ignore=shutil.ignore_patterns('__pycache__'))
        (self.repo / 'tools/release').mkdir()
        self.runner = self.repo / 'tools/build/plugin-build.sh'
        self.source = self.repo / 'plugins/example'
        (self.source / 'scripts/ci').mkdir(parents=True)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.env = dict(os.environ, PATH=str(self.bin) + ':' + os.environ['PATH'])
        for key in list(self.env):
            if any(word in key.upper() for word in ('TOKEN', 'SECRET', 'PASSWORD', 'SIGNING')):
                self.env.pop(key)
        self.tool('git', '''#!/usr/bin/env python3
import os,sys
args=sys.argv
if '--show-toplevel' in args: print(os.environ.get('FAKE_TOPLEVEL', args[args.index('-C')+1]))
elif 'rev-parse' in args: print(os.environ.get('FAKE_REVISION', 'a'*40))
elif 'status' in args: print(os.environ.get('FAKE_DIRTY',''),end='')
''')
        self.tool('go', '''#!/usr/bin/env python3
import os,pathlib,sys
if sys.argv[1]=='build':
 # Fake "go build" of tools/release: a stamp that rewrites the staged version.
 out=pathlib.Path(sys.argv[sys.argv.index('-o')+1])
 out.write_text("""#!/usr/bin/env python3
import sys,pathlib,json
a=sys.argv; f=pathlib.Path(a[a.index('--dir')+1])/'plugin.json'; m=json.loads(f.read_text())
if m['id']!=a[a.index('--id')+1]: sys.exit(3)
m['version']=a[a.index('--version')+1]; f.write_text(json.dumps(m))
""")
 out.chmod(0o755)
 sys.exit(0)
pathlib.Path(os.environ['FAKE_GO_LOG']).write_text(' '.join(sys.argv[1:]))
if os.environ.get('FAKE_SDK_INSTALL_FAIL'): sys.exit(9)
p=pathlib.Path(os.environ['GOBIN'])/'plugin-sdk'
p.write_text("""#!/usr/bin/env python3
import sys,pathlib,json,tarfile,hashlib,os
args=sys.argv; stage=pathlib.Path(args[args.index('--dir')+1]); m=json.loads((stage/'plugin.json').read_text())
if args[1]=='pack':
 out=pathlib.Path(args[args.index('--out')+1]); out.mkdir(exist_ok=True)
 sums=''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+m['id']+'/'+p.name+chr(10) for p in sorted(stage.iterdir()) if p.is_file())
 (stage/'SHA256SUMS').write_text(sums)
 (stage/'.unsigned').touch()
 if os.environ.get('FAKE_BAD_CHECKSUM'): (stage/'SHA256SUMS').write_text('')
 if os.environ.get('FAKE_MISSING_MARKER'): (stage/'.unsigned').unlink()
 with tarfile.open(out/(m['id']+'-'+m['version']+'.tar.gz'),'w:gz') as t: t.add(stage,arcname=m['id'])
""")
p.chmod(0o755)
''')
        self.env['FAKE_GO_LOG'] = str(self.root / 'go.log')
        self.hook('')

    def tool(self, name, code):
        path = self.bin / name
        path.write_text(code)
        path.chmod(0o755)

    def hook(self, extra):
        (self.source / 'scripts/ci/plugin-build-v1.sh').write_text('''#!/usr/bin/env bash
set -euo pipefail
test "$PWD" = "$(cd "$(dirname "$0")/../.." && pwd)"  # runs from the plugin folder
printf '%s' '{"contract":2,"kind":"process","id":"example","version":"0.0.0","binary":"example"}' > "$1/plugin.json"
cp /bin/true "$1/example"
''' + extra + '\n')

    def run_build(self, *extra):
        return subprocess.run(['bash', str(self.runner), '--plugin-dir', 'plugins/example', '--revision', SHA,
                               '--id', 'example', '--version', '1.2.3',
                               '--out', str(self.root / 'dist'), '--platform', 'linux-amd64', *extra],
                              env=self.env, text=True, capture_output=True)

    def test_success(self):
        result = self.run_build()
        self.assertEqual(result.returncode, 0, result.stderr)
        provenance = json.loads((self.root / 'dist/provenance.json').read_text())
        self.assertEqual(provenance['sourceRevision'], SHA)
        self.assertEqual(provenance['runnerRevision'], SHA)
        self.assertEqual(provenance['sdkVersion'], 'v2.0.0')
        self.assertEqual(provenance['version'], '1.2.3')
        self.assertEqual(provenance['pluginDir'], 'plugins/example')
        self.assertIn('bytedesk-remote-gateway-plugin-sdk/v2/cmd/plugin-sdk@v2.0.0', (self.root / 'go.log').read_text())
        self.assertTrue((self.root / 'dist/example-1.2.3.tar.gz').is_file())
        self.assertTrue((self.root / 'dist/SHA256SUMS').is_file())

    def test_v1_sdk_pin_uses_root_module(self):
        result = self.run_build('--sdk-version', 'v0.4.0-rc.11')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('plugin-sdk/cmd/plugin-sdk@v0.4.0-rc.11', (self.root / 'go.log').read_text())

    def test_plugin_dir_outside_plugins(self):
        result = self.run_build('--plugin-dir', 'tools/build')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('plugins/<name>', result.stderr)

    def test_plugin_dir_escape(self):
        self.assertNotEqual(self.run_build('--plugin-dir', '../repo/plugins/example').returncode, 0)

    def test_plugin_dir_absolute(self):
        self.assertNotEqual(self.run_build('--plugin-dir', str(self.source)).returncode, 0)

    def test_stamp_is_staging_only(self):
        self.assertEqual(self.run_build().returncode, 0)
        self.assertFalse((self.source / 'plugin.json').exists())

    def test_revision_mismatch(self):
        self.env['FAKE_REVISION'] = 'b' * 40
        self.assertNotEqual(self.run_build().returncode, 0)

    def test_dirty_checkout(self):
        self.env['FAKE_DIRTY'] = ' M plugin.json\n'
        self.assertNotEqual(self.run_build().returncode, 0)

    def test_nested_directory_is_not_a_checkout_root(self):
        self.env['FAKE_TOPLEVEL'] = str(self.root)  # repo is not the checkout root
        result = self.run_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('checkout root', result.stderr)
        self.assertFalse((self.root / 'dist').exists())

    def test_symlink(self):
        self.hook('ln -s /etc/passwd "$1/leak"')
        result = self.run_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('symlink', result.stderr)

    def test_special_file(self):
        self.hook('mkfifo "$1/pipe"')
        self.assertNotEqual(self.run_build().returncode, 0)

    def test_identity_mismatch(self):
        result = self.run_build('--id', 'different')
        self.assertNotEqual(result.returncode, 0)

    def test_mutable_sdk_pin(self):
        self.assertNotEqual(self.run_build('--sdk-version', 'latest').returncode, 0)

    def test_existing_output(self):
        (self.root / 'dist').mkdir()
        self.assertNotEqual(self.run_build().returncode, 0)

    def test_failed_hook(self):
        self.hook('exit 17')
        self.assertNotEqual(self.run_build().returncode, 0)
        self.assertFalse((self.root / 'dist').exists())

    def test_no_publication_credentials(self):
        self.env['STORE_ADMIN_TOKEN'] = 'fixture-not-a-secret'
        result = self.run_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('fixture-not-a-secret', result.stderr)

    def test_wrong_platform(self):
        result = self.run_build('--platform', 'linux-arm64')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('platform mismatch', result.stderr)

    def test_no_output_inside_source(self):
        self.assertNotEqual(self.run_build('--out', str(self.source / 'dist')).returncode, 0)
        self.assertNotEqual(self.run_build('--out', str(self.repo / 'dist')).returncode, 0)

    def test_sdk_failure(self):
        self.env['FAKE_SDK_INSTALL_FAIL'] = '1'
        self.assertNotEqual(self.run_build().returncode, 0)
        self.assertFalse((self.root / 'dist').exists())

    def test_missing_binary(self):
        self.hook('rm "$1/example"')
        self.assertNotEqual(self.run_build().returncode, 0)

    def test_no_arbitrary_command(self):
        self.assertNotEqual(self.run_build('--command', 'true').returncode, 0)

    def test_bad_member_checksums(self):
        self.env['FAKE_BAD_CHECKSUM'] = '1'
        result = self.run_build()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('checksums mismatch', result.stderr)

    def test_missing_sdk_marker(self):
        self.env['FAKE_MISSING_MARKER'] = '1'
        self.assertNotEqual(self.run_build().returncode, 0)

    def test_hook_receives_platform(self):
        self.hook('test "$PLUGIN_BUILD_PLATFORM" = linux-amd64')
        result = self.run_build()
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == '__main__':
    unittest.main()
