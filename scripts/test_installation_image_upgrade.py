"""Opt-in live Core image transition using only a fixture-owned loopback registry."""
import json
import os
from pathlib import Path
import platform
import secrets
import shutil
import subprocess
import tempfile
import unittest
import uuid
from unittest.mock import patch

from scripts.installation.lifecycle import operate
from scripts.installation.prepare import prepare
from scripts.installation.restore import restore
from scripts.installation.backup import verify
from scripts import test_installation_preflight
from scripts.test_installation_database import POSTGRES


@unittest.skipUnless(os.environ.get('BRAIDPLANE_INSTALL_IMAGE_TEST') == '1', 'requires locally built Core images and disposable Docker fixtures')
class ImageUpgradeTest(unittest.TestCase):
    def test_running_core_image_upgrade(self):
        self.check_running_core_image_upgrade()

    def test_failed_upgrade_restores_prior_schema_under_recovery_hold(self):
        self.check_running_core_image_upgrade(fail_grants=True)

    def check_running_core_image_upgrade(self, fail_grants=False):
        def failure_output(error):
            while error.__cause__ is not None:
                error = error.__cause__
            return str(getattr(error, 'stderr', error))

        def docker(*args, **kwargs):
            result = subprocess.run(['docker', *args], capture_output=True, **kwargs)
            if result.returncode:
                self.fail(result.stderr.decode())
            return result.stdout.decode().strip()
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        identity = 'recovery-images-' + uuid.uuid4().hex[:12]
        registry = identity + '-registry'
        registry_image = docker('image', 'inspect', 'registry:2', '--format', '{{.Id}}')
        docker('run', '--detach', '--name', registry, '-p', '127.0.0.1::5000', registry_image)
        self.addCleanup(docker, 'rm', '--force', '--volumes', registry)
        bindings = json.loads(docker('inspect', registry, '--format', '{{json .NetworkSettings.Ports}}'))
        endpoint = 'localhost:' + bindings['5000/tcp'][0]['HostPort']
        def publish(name, image):
            reference = endpoint + '/' + name + ':fixture'
            docker('tag', image, reference)
            docker('push', reference)
            digests = json.loads(docker('image', 'inspect', reference, '--format', '{{json .RepoDigests}}'))
            return next(digest for digest in digests if digest.startswith(endpoint + '/'))
        def variant(name, base, extra):
            context = root / name
            context.mkdir()
            (context / 'Dockerfile').write_text('FROM ' + base + '\n' + extra + '\n')
            tag = identity + '-' + name + ':local'
            docker('build', '-t', tag, str(context))
            return tag
        old_prefix = os.environ.get('BRAIDPLANE_INSTALL_OLD_IMAGE_PREFIX', 'braidplane-recovery')
        new_prefix = os.environ.get('BRAIDPLANE_INSTALL_NEW_IMAGE_PREFIX', old_prefix)
        old = variant('old', old_prefix + '-backend:local', 'LABEL recovery.fixture.version="old"')
        new = variant('new', new_prefix + '-backend:local', 'LABEL recovery.fixture.version="new"')
        images = {name: publish(name, old_prefix + '-' + name + ':local') for name in ('frontend', 'migrate', 'cli')}
        images.update(backend=publish('backend-old', old), edge=publish('edge', 'caddy:2'), postgres=publish('postgres', POSTGRES))
        revision = subprocess.run(['git', 'rev-parse', 'HEAD'], check=True, capture_output=True, text=True).stdout.strip()
        old_revision = docker('image', 'inspect', old, '--format', '{{index .Config.Labels "org.opencontainers.image.revision"}}') or revision
        new_revision = docker('image', 'inspect', new, '--format', '{{index .Config.Labels "org.opencontainers.image.revision"}}') or revision
        release = {'version': 'v0.1.0-alpha.1', 'core_revision': old_revision,
                   'platform': 'linux/arm64' if platform.machine() == 'arm64' else 'linux/amd64', 'images': images}
        first = root / 'first.json'
        first.write_text(json.dumps(release))
        second_images = dict(images, backend=publish('backend-new', new))
        if old_prefix != new_prefix:
            second_images.update({name: publish(name + '-new', new_prefix + '-' + name + ':local')
                                  for name in ('frontend', 'migrate', 'cli')})
        second_release = dict(release, version='v0.1.0-alpha.2', core_revision=new_revision, images=second_images)
        second = root / 'second.json'
        second.write_text(json.dumps(second_release))
        bundle = root / 'candidate-bundle'
        (bundle / 'scripts/installation').mkdir(parents=True)
        (bundle / 'release.json').write_text(json.dumps(second_release))
        source = Path(__file__).resolve().parent
        shutil.copyfile(source / 'self-hosted-runtime-grants.sql', bundle / 'scripts/self-hosted-runtime-grants.sql')
        shutil.copyfile(source / 'installation/execution-grants.sql', bundle / 'scripts/installation/execution-grants.sql')
        if fail_grants:
            with (bundle / 'scripts/installation/execution-grants.sql').open('a') as output:
                output.write('\nSELECT fixture_missing_grant_capability();\n')
        tls = test_installation_preflight.TLSPreflightTest()
        tls.setUp()
        self.addCleanup(tls.doCleanups)
        installation = root / 'installation'
        prepare(installation, first, 'app.customer.test', '127.0.0.1', 'mail.customer.test:587', 'owner@customer.test', tls.certs, api_only=True)
        if old_prefix != new_prefix:
            grants = (installation / 'execution-grants.sql').read_text()
            old_grants = grants[:grants.index('    public.begin_retained_file(')].rstrip().removesuffix(',')
            (installation / 'execution-grants.sql').write_text(old_grants + '\nTO :"executor_role";\nCOMMIT;\n')
        topology = json.loads((installation / 'compose.json').read_text())
        topology['services']['edge']['ports'] = ['127.0.0.1::443']
        (installation / 'compose.json').write_text(json.dumps(topology))
        command = ['compose', '--env-file', str(installation / 'release.env'), '-f', str(installation / 'compose.json')]
        def compose(*args, **kwargs):
            return docker(*command, *args, cwd=installation, **kwargs)
        self.addCleanup(compose, 'down', '--volumes')
        operate(installation, 'install')
        # Bootstrap and retain real application state, keeping the generated password on stdin.
        compose('run', '--rm', '--no-deps', '-T', 'bootstrap', 'bootstrap', '--user', 'recovery-owner',
                '--name', 'Recovery Owner', '--email', 'owner@customer.test', '--email-ownership-verified',
                input=(secrets.token_hex(24) + '\n').encode())
        keys = (installation / 'runtime.env').read_bytes()
        def api_image():
            container = compose('ps', '-q', 'api')
            return docker('inspect', container, '--format', '{{.Image}}')
        def state():
            return compose('exec', '-T', 'database', 'psql', '-U', 'braidplane_operator', '-d', 'braidplane',
                           '-At', '-c', "SELECT slug FROM public.users WHERE slug='recovery-owner'")
        old_image = docker('image', 'inspect', old, '--format', '{{.Id}}')
        new_image = docker('image', 'inspect', new, '--format', '{{.Id}}')
        self.assertNotEqual(old_image, new_image)
        self.assertEqual(api_image(), old_image)
        self.assertEqual(state(), 'recovery-owner')
        previous_schema = compose('exec', '-T', 'database', 'psql', '-U', 'braidplane_operator', '-d', 'braidplane',
                                  '-At', '-c', "SELECT version FROM core_schema_migrations; SELECT to_regprocedure('public.begin_retained_file(uuid,uuid,text,bigint,timestamptz)') IS NULL;").splitlines()
        if old_prefix != new_prefix:
            self.assertEqual(previous_schema, ['2', 't'])
        backup = root / 'before-upgrade'
        if fail_grants:
            with patch('scripts.installation.upgrade.BUNDLE', bundle):
                with self.assertRaisesRegex(ValueError, 'upgrade failed') as failed:
                    operate(installation, 'upgrade', release=second, backup_directory=backup)
            self.assertIn('fixture_missing_grant_capability', failure_output(failed.exception))
            self.assertTrue((installation / '.upgrade-in-progress.json').is_file())
            self.assertEqual(json.loads((installation / 'release.json').read_text()), release)
            self.assertEqual(json.loads((installation / '.installed-release.json').read_text()), release)
            self.assertEqual((installation / 'runtime.env').read_bytes(), keys)
            running = compose('ps', '--services', '--status', 'running').splitlines()
            self.assertEqual(running, ['database'])
            with self.assertRaisesRegex(ValueError, 'upgrade is incomplete'):
                operate(installation, 'start')
            self.assertEqual(verify(backup)['release'], release)
            for name in ('database.dump', 'configuration.tar', 'backup.json'):
                self.assertEqual((backup / name).stat().st_mode & 0o777, 0o600)
            recovered = root / 'recovered'
            def cleanup_recovered():
                if (recovered / 'compose.json').exists():
                    docker('compose', '--env-file', str(recovered / 'release.env'), '-f', str(recovered / 'compose.json'), 'down', '--volumes', cwd=recovered)
            self.addCleanup(cleanup_recovered)
            result = restore(backup, recovered)
            self.assertEqual(result['state'], 'isolated-awaiting-reconciliation')
            self.assertEqual((recovered / 'runtime.env').read_bytes(), keys)
            restored = docker('compose', '--env-file', str(recovered / 'release.env'), '-f', str(recovered / 'compose.json'),
                              'exec', '-T', 'database', 'psql', '-U', 'braidplane_operator', '-d', 'braidplane', '-At', '-c',
                              "SELECT slug FROM users WHERE slug='recovery-owner'; SELECT version FROM core_schema_migrations; SELECT to_regprocedure('public.begin_retained_file(uuid,uuid,text,bigint,timestamptz)') IS NULL;",
                              cwd=recovered).splitlines()
            self.assertEqual(restored, ['recovery-owner', *previous_schema])
            with self.assertRaisesRegex(ValueError, 'held for reconciliation'):
                operate(recovered, 'start')
            if old_prefix != new_prefix:
                version = compose('exec', '-T', 'database', 'psql', '-U', 'braidplane_operator', '-d', 'braidplane',
                                  '-At', '-c', 'SELECT version FROM core_schema_migrations')
                self.assertEqual(version, '3', 'recovery must not roll back the failed source database')
            return
        with patch('scripts.installation.upgrade.BUNDLE', bundle):
            try:
                operate(installation, 'upgrade', release=second, backup_directory=backup)
            except ValueError as error:
                self.fail(failure_output(error))
        self.assertEqual(api_image(), new_image)
        self.assertEqual(state(), 'recovery-owner')
        self.assertEqual((installation / 'runtime.env').read_bytes(), keys)
        self.assertFalse((installation / '.upgrade-in-progress.json').exists())
        if old_prefix != new_prefix:
            version = compose('exec', '-T', 'database', 'psql', '-U', 'braidplane_operator', '-d', 'braidplane',
                              '-At', '-c', 'SELECT version FROM core_schema_migrations')
            self.assertEqual(version, '3')
            capability = compose('exec', '-T', 'database', 'psql', '-U', 'braidplane_operator', '-d', 'braidplane',
                                 '-At', '-c', "SELECT has_function_privilege('braidplane_executor', 'public.begin_retained_file(uuid,uuid,text,bigint,timestamptz)', 'EXECUTE')")
            self.assertEqual(capability, 't')
