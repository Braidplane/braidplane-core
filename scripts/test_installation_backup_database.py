"""Real Compose backup into the isolated restore probe; synthetic owned storage only."""
import json
import io
import os
from pathlib import Path
import platform
import subprocess
import shutil
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from scripts.installation.backup import seal, verify
from scripts.installation.lifecycle import operate, read_inputs
from scripts.installation.prepare import prepare
from scripts.installation.restore_probe import probe
from scripts.installation.restore import restore
from scripts.test_installation_database import POSTGRES, ROOT
from scripts import test_installation_preflight
from scripts.test_installation_release import manifest


@unittest.skipUnless(os.environ.get('BRAIDPLANE_INSTALL_DATABASE_TEST') == '1',
                     'requires explicit disposable Docker fixture')
class BackupDatabaseTest(unittest.TestCase):
    def test_compose_backup_and_isolated_restore(self):
        tls = test_installation_preflight.TLSPreflightTest()
        tls.setUp()
        self.addCleanup(tls.doCleanups)
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        release = manifest()
        # Only PostgreSQL executes in this fixture; all image slots are synthetic.
        release['images'] = {name: POSTGRES.replace('postgres:18.6@', 'postgres@') for name in release['images']}
        release['platform'] = 'linux/arm64' if platform.machine() == 'arm64' else 'linux/amd64'
        release_path = root / 'release.json'
        release_path.write_text(json.dumps(release))
        installation = root / 'installation'
        prepare(installation, release_path, 'app.customer.test', '127.0.0.1',
                'mail.customer.test:587', 'owner@customer.test', tls.certs)
        grants = (installation / 'execution-grants.sql').read_text()
        old_grants = grants[:grants.index('    public.begin_retained_file(')].rstrip().removesuffix(',')
        (installation / 'execution-grants.sql').write_text(old_grants + '\nTO :"executor_role";\nCOMMIT;\n')
        (installation / '.installed-release.json').write_text(json.dumps(release))
        _, values = read_inputs(installation)
        command = ['docker', 'compose', '--project-name', values['BRAIDPLANE_PROJECT_NAME'],
                   '--env-file', str(installation / 'release.env'), '-f', str(installation / 'compose.json')]
        def compose(*args, **kwargs):
            result = subprocess.run([*command, *args], cwd=installation, capture_output=True, **kwargs)
            if result.returncode:
                # Fixture-only diagnostics contain no customer inputs.
                self.fail(result.stderr.decode())
            return result
        self.addCleanup(compose, 'down', '--volumes')
        compose('up', '--detach', '--no-deps', '--wait', '--wait-timeout', '90', 'database')
        catalog = (ROOT / 'postgres/migrations/000001_core.up.sql').read_bytes()
        compose('exec', '-T', 'database', 'psql', '-U', 'braidplane_operator', '-d', 'braidplane',
                '-v', 'ON_ERROR_STOP=1', input=b'SET ROLE braidplane_migrator;\n' + catalog +
                b'\nCREATE TABLE public.core_schema_migrations(version bigint, dirty boolean); '
                b'INSERT INTO public.core_schema_migrations VALUES (1, false); '
                b'CREATE TABLE public.recovery_oracle(id integer PRIMARY KEY, value text); '
                b"INSERT INTO public.recovery_oracle VALUES (1, 'frozen-before-backup');")
        # Independent grant application is required before the backup; dump uses the migrator login.
        compose('run', '--rm', '--no-deps', '-T', 'grants')
        destination = root / 'backup'
        operate(installation, 'backup', backup_directory=destination)
        receipt = verify(destination)
        self.assertEqual(receipt['release'], release)
        with tarfile.open(destination / 'configuration.tar') as archive:
            self.assertEqual(archive.extractfile('runtime.env').read(), (installation / 'runtime.env').read_bytes())
        compose('exec', '-T', 'database', 'psql', '-U', 'braidplane_operator', '-d', 'braidplane',
                '-v', 'ON_ERROR_STOP=1', input=b"INSERT INTO public.recovery_oracle VALUES (2, 'lost-after-backup');")
        run = subprocess.run
        inspected = []
        def observe_cleanup(command, **kwargs):
            if command[:4] == ['docker', 'rm', '--force', '--volumes']:
                restored = run(['docker', 'exec', command[-1], 'psql', '-U', 'postgres', '-d', 'braidplane',
                                '-At', '-c', 'SELECT id,value FROM public.recovery_oracle ORDER BY id'],
                               check=True, capture_output=True)
                # Independent readback uses a frozen oracle, not backup-derived expected output.
                inspected.append(restored.stdout)
            return run(command, **kwargs)
        with patch('scripts.installation.restore_probe.subprocess.run', side_effect=observe_cleanup):
            result = probe(destination)
        self.assertEqual(inspected, [b'1|frozen-before-backup\n'])
        self.assertEqual(result['schema_version'], '1')
        self.assertEqual(json.loads((installation / '.installed-release.json').read_text()), release)
        print('PASS: real Compose backup, recovered configuration bytes, isolated restore/grants, frozen oracle and one post-backup record lost; elapsed_seconds=', result['elapsed_seconds'])

        # A saved host override must not redirect the recovery client outside its new database.
        rerouted_backup = root / 'rerouted-backup'
        rerouted_backup.mkdir(mode=0o700)
        shutil.copyfile(destination / 'database.dump', rerouted_backup / 'database.dump')
        (rerouted_backup / 'database.dump').chmod(0o600)
        with tarfile.open(destination / 'configuration.tar') as archive:
            contents = {member.name: archive.extractfile(member).read() for member in archive.getmembers()}
        contents['migrator.env'] = contents['migrator.env'].replace(b'DB_HOST=database\n', b'DB_HOST=unreachable.invalid\n')
        contents['migrator.env'] += b'PGHOSTADDR=192.0.2.1\nPGSERVICE=unconfigured-recovery-service\n'
        with tarfile.open(rerouted_backup / 'configuration.tar', 'w') as archive:
            for name, content in contents.items():
                member = tarfile.TarInfo(name)
                member.size = len(content)
                archive.addfile(member, io.BytesIO(content))
        (rerouted_backup / 'configuration.tar').chmod(0o600)
        seal(rerouted_backup, release, receipt['writers_stopped_at'])
        recovered = root / 'recovered'
        def cleanup_recovered():
            if not (recovered / 'release.env').exists():
                return
            _, restored_values = read_inputs(recovered)
            subprocess.run(['docker', 'compose', '--project-name', restored_values['BRAIDPLANE_PROJECT_NAME'],
                            '--env-file', str(recovered / 'release.env'), '-f', str(recovered / 'compose.json'),
                            'down', '--volumes'], cwd=recovered, capture_output=True, check=True)
        self.addCleanup(cleanup_recovered)
        try:
            persistent = restore(rerouted_backup, recovered)
        except subprocess.CalledProcessError as error:
            self.fail(error.stderr.decode())
        self.assertEqual(persistent['state'], 'isolated-awaiting-reconciliation')
        self.assertNotEqual(persistent['project'], values['BRAIDPLANE_PROJECT_NAME'])
        topology = json.loads((recovered / 'compose.json').read_text())
        self.assertEqual(set(topology['services']), {'database', 'grants'})
        self.assertTrue(topology['networks']['private']['internal'])
        self.assertEqual(topology['services']['grants']['environment']['DB_HOST'], 'database')
        self.assertEqual(topology['volumes'], {'database': {}})
        self.assertEqual((recovered / 'runtime.env').read_bytes(), (installation / 'runtime.env').read_bytes())
        readback = subprocess.run(['docker', 'compose', '--project-name', persistent['project'],
                                   '--env-file', str(recovered / 'release.env'), '-f', str(recovered / 'compose.json'),
                                   'exec', '-T', 'database', 'psql', '-U', 'braidplane_operator', '-d', 'braidplane',
                                   '-At', '-c', 'SELECT id,value FROM public.recovery_oracle ORDER BY id'],
                                  cwd=recovered, check=True, capture_output=True)
        self.assertEqual(readback.stdout, b'1|frozen-before-backup\n')
        with self.assertRaisesRegex(ValueError, 'held for reconciliation'):
            operate(recovered, 'start')
        with self.assertRaises(FileExistsError):
            restore(destination, recovered)
        print('PASS: persistent isolated restore with new project identity, retained keys and startup hold; elapsed_seconds=', persistent['elapsed_seconds'])

        compose('exec', '-T', 'database', 'psql', '-U', 'braidplane_operator', '-d', 'braidplane',
                '-v', 'ON_ERROR_STOP=1', input=b'UPDATE public.core_schema_migrations SET dirty=true;')
        dirty_backup = root / 'dirty-backup'
        operate(installation, 'backup', backup_directory=dirty_backup)
        with self.assertRaisesRegex(ValueError, 'exactly one clean version'):
            probe(dirty_backup)

        # Simulate a migrator that persists its dirty fence and then exits unsuccessfully.
        compose('exec', '-T', 'database', 'psql', '-U', 'braidplane_operator', '-d', 'braidplane',
                '-v', 'ON_ERROR_STOP=1', input=b'UPDATE public.core_schema_migrations SET dirty=false;')
        topology = json.loads((installation / 'compose.json').read_text())
        migrator = dict(topology['services']['grants'])
        migrator.pop('depends_on', None)
        migrator['command'] = ["export PGHOST=$$DB_HOST PGPORT=$$DB_PORT PGUSER=$$DB_USER PGPASSWORD=$$DB_PASSWORD PGDATABASE=$$DB_DATABASE PGSSLMODE=$$DB_SSL_MODE PGSSLROOTCERT=$$DB_SSL_ROOT_CERT; psql -v ON_ERROR_STOP=1 -c 'UPDATE public.core_schema_migrations SET dirty=true'; exit 1"]
        topology['services']['migrate'] = migrator
        (installation / 'compose.json').write_text(json.dumps(topology))
        candidate = dict(release, version='v0.1.0-alpha.2')
        candidate_path = root / 'candidate.json'
        candidate_path.write_text(json.dumps(candidate))
        bundle = root / 'candidate-bundle'
        (bundle / 'scripts/installation').mkdir(parents=True)
        (bundle / 'release.json').write_text(json.dumps(candidate))
        shutil.copyfile(ROOT / 'scripts/self-hosted-runtime-grants.sql', bundle / 'scripts/self-hosted-runtime-grants.sql')
        shutil.copyfile(ROOT / 'scripts/installation/execution-grants.sql', bundle / 'scripts/installation/execution-grants.sql')
        upgrade_backup = root / 'before-failed-migration'
        with patch('scripts.installation.upgrade.BUNDLE', bundle), self.assertRaisesRegex(ValueError, 'no automatic rollback'):
            operate(installation, 'upgrade', release=candidate_path, backup_directory=upgrade_backup)
        self.assertTrue((installation / '.upgrade-in-progress.json').is_file())
        self.assertEqual(json.loads((installation / '.installed-release.json').read_text()), release)
        with self.assertRaisesRegex(ValueError, 'upgrade is incomplete'):
            operate(installation, 'start')
        dirty = compose('exec', '-T', 'database', 'psql', '-U', 'braidplane_operator', '-d', 'braidplane',
                        '-At', '-c', 'SELECT dirty FROM public.core_schema_migrations').stdout.strip()
        self.assertEqual(dirty, b't')
        self.assertEqual(probe(upgrade_backup)['schema_version'], '1')
        running = json.loads(compose('ps', '--format', 'json', '--status', 'running').stdout)
        self.assertEqual(running['Service'], 'database')
        print('PASS: injected migration failure retains dirty fence/journal and old release, blocks startup, and preserves a restorable clean pre-migration backup')
