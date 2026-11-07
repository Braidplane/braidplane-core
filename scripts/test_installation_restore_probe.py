import io
import os
import platform
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from scripts.installation.backup import seal
from scripts.installation.restore_probe import probe
from scripts.installation.upgrade import SNAPSHOT
from scripts.test_installation_release import manifest


class RestoreProbeTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.release = manifest()
        (self.root / 'database.dump').write_bytes(b'synthetic dump')
        with tarfile.open(self.root / 'configuration.tar', 'w') as archive:
            names = list(SNAPSHOT) + ['certificates/' + name for name in
                                     ('ca.pem', 'database.pem', 'database.key', 'edge.pem', 'edge.key')]
            for name in names:
                content = json.dumps(self.release).encode() if name == 'release.json' else b'synthetic fixture'
                info = tarfile.TarInfo(name)
                info.size = len(content)
                archive.addfile(info, io.BytesIO(content))
        seal(self.root, self.release, '2026-01-01T00:00:00+00:00')
        self.calls = []
        def run(command, **kwargs):
            self.calls.append(command)
            sql = kwargs.get('input', b'')
            output = b't\n' if b'count(*)' in sql else b'1\n'
            return subprocess.CompletedProcess(command, 0, stdout=output)
        self.run = self.enterContext(patch('scripts.installation.restore_probe.subprocess.run', side_effect=run))

    def test_restore_is_network_isolated_uses_retained_image_and_cleans_up(self):
        result = probe(self.root)
        self.assertEqual(result['schema_version'], '1')
        self.assertIn('none', self.calls[0])
        self.assertEqual(self.calls[0][-1], self.release['images']['postgres'])
        restore = next(call for call in self.calls if 'pg_restore' in call)
        self.assertIn('--single-transaction', restore)
        self.assertIn('--role=braidplane_migrator', restore)
        self.assertEqual(self.calls[-1][1:4], ['rm', '--force', '--volumes'])
        self.assertFalse(any('compose' in call for call in self.calls))

    def test_restore_failure_still_removes_owned_container(self):
        original = self.run.side_effect
        def fail(command, **kwargs):
            if 'pg_restore' in command:
                raise subprocess.CalledProcessError(1, command)
            return original(command, **kwargs)
        self.run.side_effect = fail
        with self.assertRaises(subprocess.CalledProcessError):
            probe(self.root)
        self.assertEqual(self.calls[-1][1:4], ['rm', '--force', '--volumes'])

    def test_corrupt_dump_is_refused_before_docker(self):
        (self.root / 'database.dump').write_bytes(b'changed')
        with self.assertRaisesRegex(ValueError, 'integrity mismatch'):
            probe(self.root)
        self.run.assert_not_called()

    def test_unexpected_archive_member_is_refused_before_docker(self):
        with tarfile.open(self.root / 'configuration.tar', 'a') as archive:
            member = tarfile.TarInfo('../escaped')
            archive.addfile(member)
        (self.root / 'backup.json').unlink()
        seal(self.root, self.release, '2026-01-01T00:00:00+00:00')
        with self.assertRaisesRegex(ValueError, 'inventory mismatch'):
            probe(self.root)
        self.run.assert_not_called()


class RestoreProbeDatabaseTest(unittest.TestCase):
    @unittest.skipUnless(os.environ.get('BRAIDPLANE_INSTALL_DATABASE_TEST') == '1',
                         'requires explicit disposable Docker fixture')
    def test_real_catalog_dump_and_isolated_restore(self):
        from scripts.test_installation_database import POSTGRES, ROOT
        import time
        import uuid
        container = 'bp1324-source-' + uuid.uuid4().hex[:12]
        def docker(*args, **kwargs):
            result = subprocess.run(['docker', *args], capture_output=True, **kwargs)
            if result.returncode:
                self.fail(result.stderr.decode())
            return result
        docker('run', '--detach', '--name', container, '--network', 'none',
               '-e', 'POSTGRES_HOST_AUTH_METHOD=trust', '-e', 'POSTGRES_DB=braidplane', POSTGRES)
        self.addCleanup(docker, 'rm', '--force', '--volumes', container)
        for _ in range(120):
            try:
                subprocess.run(['docker', 'exec', container, 'pg_isready', '-h', '127.0.0.1', '-U', 'postgres'], check=True, capture_output=True)
                break
            except subprocess.CalledProcessError:
                time.sleep(0.5)
        else:
            self.fail('source database did not become ready')
        init = (b'CREATE ROLE braidplane_migrator LOGIN; CREATE ROLE braidplane_runtime LOGIN; '
                b'CREATE ROLE braidplane_executor LOGIN; ALTER DATABASE braidplane OWNER TO braidplane_migrator; '
                b'ALTER SCHEMA public OWNER TO braidplane_migrator;')
        catalog = (ROOT / 'postgres/migrations/000001_core.up.sql').read_bytes()
        docker('exec', '-i', container, 'psql', '-U', 'postgres', '-d', 'braidplane', '-v', 'ON_ERROR_STOP=1',
               input=init + b' SET ROLE braidplane_migrator;\n' + catalog +
               b'\nCREATE TABLE public.core_schema_migrations(version bigint, dirty boolean); '
               b'INSERT INTO public.core_schema_migrations VALUES (1, false);')
        dump = docker('exec', container, 'pg_dump', '-U', 'postgres', '-d', 'braidplane',
                      '--format=custom', '--no-owner', '--no-acl').stdout
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            release = manifest()
            release['images']['postgres'] = POSTGRES.replace('postgres:18.6@', 'postgres@')
            release['platform'] = 'linux/arm64' if platform.machine() == 'arm64' else 'linux/amd64'
            (directory / 'database.dump').write_bytes(dump)
            files = {name: b'synthetic fixture' for name in SNAPSHOT}
            files.update({'certificates/' + name: b'synthetic fixture' for name in
                          ('ca.pem', 'database.pem', 'database.key', 'edge.pem', 'edge.key')})
            files['release.json'] = json.dumps(release).encode()
            files['init.sql'] = init
            files['runtime-grants.sql'] = (ROOT / 'scripts/self-hosted-runtime-grants.sql').read_bytes()
            files['execution-grants.sql'] = (ROOT / 'scripts/installation/execution-grants.sql').read_bytes()
            with tarfile.open(directory / 'configuration.tar', 'w') as archive:
                for name, content in files.items():
                    info = tarfile.TarInfo(name)
                    info.size = len(content)
                    archive.addfile(info, io.BytesIO(content))
            seal(directory, release, '2026-01-01T00:00:00+00:00')
            result = probe(directory)
            self.assertEqual(result['schema_version'], '1')
            print('Isolated real-catalog restore:', json.dumps(result, sort_keys=True))
