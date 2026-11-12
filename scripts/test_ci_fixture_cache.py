import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class FixtureImageCache(unittest.TestCase):
    def test_cold_restore_and_failures(self):
        action = (ROOT / '.github/actions/cache-fixture-images/action.yml').read_text()
        block = action.split('      run: |\n', 1)[1].split('    - uses:', 1)[0]
        script = '\n'.join(line[8:] for line in block.splitlines())
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            docker = root / 'docker'
            docker.write_text('''#!/bin/bash
printf '%s\\n' "$*" >> "$CALLS"
case "$1 $2" in
  'compose -f')
    if [[ "$MODE" = compose-failure ]]; then exit 42; fi
    if [[ "$MODE" = empty ]]; then exit 0; fi
    printf 'postgres:18.6@sha256:abc\\naxllent/mailpit:v1.30@sha256:def\\nmysql:26.7@sha256:ghi\\nfixture-local-build\\npostgres:18.6@sha256:abc\\n' ;;
  'pull postgres:18.6@sha256:abc')
    if [[ "$MODE" = pull-failure || "$MODE" = corrupt-pull-failure ]]; then exit 43; fi ;;
  'image load') if [[ "$MODE" = corrupt || "$MODE" = corrupt-pull-failure ]]; then exit 44; fi ;;
  'image save') touch "$4" ;;
esac
''')
            docker.chmod(0o755)
            calls = root / 'calls'
            archive = root / 'images.tar'
            env = os.environ | {'PATH': f'{root}:{os.environ["PATH"]}',
                                'IMAGE_ARCHIVE': str(archive), 'CALLS': str(calls)}
            for mode in ('cold', 'disabled', 'hit', 'corrupt', 'corrupt-pull-failure', 'compose-failure', 'pull-failure', 'empty'):
                with self.subTest(mode=mode):
                    calls.unlink(missing_ok=True)
                    archive.unlink(missing_ok=True)
                    if mode in ('hit', 'corrupt', 'corrupt-pull-failure'):
                        archive.touch()
                    result = subprocess.run(['bash', '-euo', 'pipefail', '-c', script],
                                            env=env | {'MODE': mode, 'CACHE_ENABLED': str(mode != 'disabled').lower(), 'CACHE_HIT': str(mode in ('hit', 'corrupt', 'corrupt-pull-failure')).lower()},
                                            capture_output=True, text=True)
                    output = calls.read_text()
                    if mode in ('cold', 'disabled', 'hit', 'corrupt'):
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertIn('pull postgres:18.6@sha256:abc', output)
                        self.assertIn('pull axllent/mailpit:v1.30@sha256:def', output)
                        self.assertIn('pull mysql:26.7@sha256:ghi', output)
                        self.assertEqual(output.count('pull postgres:18.6@sha256:abc'), 1)
                        self.assertNotIn('pull fixture-local-build', output)
                        self.assertIn('compose -f docker-compose.yml -f docker-compose.connectors.yml config --images', output)
                        self.assertEqual('image load' in output, mode in ('hit', 'corrupt'))
                        if mode == 'corrupt':
                            self.assertIn('Fixture image cache could not be loaded', result.stdout)
                        self.assertEqual('image save' in output, mode == 'cold')
                        if mode == 'cold':
                            self.assertIn('axllent/mailpit:v1.30 mysql:26.7 postgres:18.6', output)
                            self.assertNotIn('sha256:', output.split('image save')[1])
                    else:
                        self.assertNotEqual(result.returncode, 0)
                        self.assertNotIn('image save', output)
                        if mode == 'corrupt-pull-failure':
                            self.assertEqual(result.returncode, 43)
                            self.assertIn('Fixture image cache could not be loaded', result.stdout)
                            self.assertIn('pull postgres:18.6@sha256:abc', output)


    def test_cold_dispatch_bypasses_all_ci_caches(self):
        workflow = (ROOT / '.github/workflows/ci.yml').read_text()
        self.assertIn('type: boolean\n        default: false', workflow)
        self.assertEqual(workflow.count('uses: actions/cache/restore@'),
                         workflow.count('if: ${{ !inputs.cold_cache }}'))
        self.assertEqual(workflow.count('uses: actions/cache/save@'),
                         workflow.count('!inputs.cold_cache && !cancelled()'))
        self.assertEqual(workflow.count('uses: ./.github/actions/cache-fixture-images'),
                         workflow.count('enabled: ${{ !inputs.cold_cache }}'))
        self.assertEqual(workflow.count('cache-from:'), workflow.count('cache-from: ${{ !inputs.cold_cache'))
        self.assertEqual(workflow.count('cache-to:'), workflow.count('cache-to: ${{ !inputs.cold_cache'))
        self.assertEqual(workflow.count('uses: docker/build-push-action@'),
                         workflow.count('no-cache: ${{ inputs.cold_cache == true }}'))
        action = (ROOT / '.github/actions/cache-fixture-images/action.yml').read_text()
        self.assertIn("if: inputs.enabled == 'true'", action)


class HostedRunnerPreparation(unittest.TestCase):
    def test_cleanup_is_hosted_only_and_limited_to_unused_sdks(self):
        action = (ROOT / '.github/actions/prepare-hosted-runner/action.yml').read_text()
        self.assertIn("if: runner.environment == 'github-hosted'", action)
        block = action.split('      run: |\n', 1)[1]
        script = '\n'.join(line[8:] for line in block.splitlines())
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            calls = root / 'calls'
            for name in ('sudo', 'df'):
                executable = root / name
                executable.write_text("#!/bin/bash\nprintf '%s\\n' \"$*\" >> \"$CALLS\"\n")
                executable.chmod(0o755)
            result = subprocess.run(['bash', '-euo', 'pipefail', '-c', script],
                                    env=os.environ | {'PATH': f'{root}:{os.environ["PATH"]}', 'CALLS': str(calls)},
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(calls.read_text().splitlines(), [
                '-h /', 'rm -rf /usr/local/lib/android /usr/share/dotnet /opt/ghc', '-h /'])


if __name__ == '__main__':
    unittest.main()
