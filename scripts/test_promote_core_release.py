import io
import json
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.core_release_version import notes
from scripts.promote_core_release import promote
from scripts.publish_core_release import ASSETS, release_tag
from scripts.test_installation_release import manifest
from scripts.validate_core_candidate import file_hash


class PromoteCoreReleaseTest(unittest.TestCase):
    platform = 'linux/amd64'

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.fixture = self.root / 'assets'
        self.fixture.mkdir()
        self.output = self.root / 'promotion'
        self.checkout = Path(__file__).resolve().parents[1]
        self.rc = 'v0.1.0-rc.2'
        self.source = 'a' * 40
        self.env = dict(GITHUB_REPOSITORY='Example/core', GITHUB_SHA='b' * 40, GITHUB_WORKFLOW_SHA='b' * 40,
                        GITHUB_REF='refs/heads/main', GITHUB_EVENT_NAME='workflow_dispatch', RUNNER_ENVIRONMENT='github-hosted',
                        GITHUB_WORKFLOW_REF='Example/core/.github/workflows/core-release-promote.yml@refs/heads/main',
                        CORE_RELEASE_PUBLICATION_AUTHORIZED='yes', DOCKER_CONFIG=str(self.root))
        self.plan = dict(previous='v0.0.0', version=self.rc, bump='minor', security=False, supersedes=[], changes=[
            dict(kind='fix', summary='Repair retries (#1)'), dict(kind='breaking', summary='Change the option (#2)')])
        release = dict(manifest(), version=self.rc, core_revision=self.source, platform=self.platform)
        self.write('release.json', release)
        self.write('release-plan.json', self.plan)
        inputs = dict(since_sha='c' * 40, through_sha=self.source, pull_requests=[])
        self.write('release-inputs.json', inputs)
        (self.fixture / 'CHANGELOG.md').write_text(notes(self.plan))
        metadata = {name: (self.fixture / name).read_bytes() for name in ('release-plan.json', 'release-inputs.json', 'CHANGELOG.md')}
        with tarfile.open(self.fixture / 'installation.tar.gz', 'w:gz') as archive:
            for name, data in dict(metadata, **{'release.json': json.dumps(release).encode()}).items():
                info = tarfile.TarInfo(f"braidplane-{self.rc}-{self.platform.replace('/', '-')}/{name}")
                info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
        record = dict(release=release, migration=dict(version=3, digest='d' * 64), archive_sha256=file_hash(self.fixture / 'installation.tar.gz'))
        self.write('record.json', record)
        materials = {name: file_hash(self.fixture / name) for name in metadata}
        self.write('approval.json', dict(record_sha256=file_hash(self.fixture / 'record.json'), metadata_sha256=materials))
        self.value = dict(repository='Example/core', source=self.source, record=record, plan=self.plan, inputs=inputs,
                          materials=materials, approval_sha256=file_hash(self.fixture / 'approval.json'), run_id='123')
        self.write('identity.json', self.value)
        self.run = dict(status='completed', conclusion='success', head_sha=self.source, event='workflow_dispatch',
                        path='.github/workflows/core-release.yml', head_branch='main', run_attempt=2)
        self.versions = [self.rc]
        self.tags = {self.rc: self.tag(self.value, "2026-01-02T00:00:00Z")}
        self.history = [self.source]

    def write(self, name, value):
        (self.fixture / name).write_text(json.dumps(value))

    def tag(self, value, date):
        return dict(object=dict(type='commit', sha=value['source']), message=json.dumps(value), tagger=dict(date=date))

    def add_rc(self, version, date, run_id='122', previous='v0.0.0', source=None):
        source = source or self.source
        value = dict(self.value, source=source, run_id=run_id, plan=dict(self.plan, previous=previous),
                     record=dict(self.value['record'], release=dict(self.value['record']['release'], version=version, core_revision=source)))
        self.tags[version] = self.tag(value, date)
        self.versions.append(version)

    def api(self, path):
        if '/git/ref/' in path:
            return dict(object=dict(type='tag', sha=self.rc))
        if '/git/tags/' in path:
            return self.tags[path.rsplit('/', 1)[-1]]
        if '/matching-refs/' in path:
            return [dict(ref=f"refs/tags/core-published/{version}/{self.platform.replace('/', '-')}", object=dict(type='tag', sha=version)) for version in self.versions]
        if '/actions/runs/' in path:
            return self.run
        raise AssertionError(path)

    def download(self, command, **kwargs):
        if command[:3] == ['gh', 'release', 'download']:
            self.assertEqual(command[3], release_tag(self.rc, self.platform))
            name = command[command.index('--pattern') + 1]
            shutil.copyfile(self.fixture / name, self.output / name)
        elif command[0] != 'git':
            raise AssertionError(command)

    def execute(self):
        with patch('scripts.promote_core_release.api', side_effect=self.api), \
             patch('scripts.promote_core_release.subprocess.check_output', side_effect=[self.env['GITHUB_SHA'], '', '\n'.join(self.history)]), \
             patch('scripts.promote_core_release.subprocess.run', side_effect=self.download):
            promote(self.rc, self.platform, self.checkout, self.output, self.env)

    def test_promotes_combined_notes_and_unchanged_verified_assets(self):
        with patch('scripts.promote_core_release.verify_release') as verify, \
             patch('scripts.promote_core_release.bind') as bind, \
             patch('scripts.promote_core_release.publish_release') as publish:
            self.execute()
        self.assertEqual(verify.call_args.args[2]['version'], self.rc)
        self.assertEqual(verify.call_args.args[2]['attempt'], '2')
        body = publish.call_args.args[3]
        self.assertIn('## v0.1.0\n', body)
        self.assertIn('Repair retries (#1)', body)
        self.assertIn('**Breaking change:** Change the option (#2)', body)
        self.assertFalse(publish.call_args.args[5])
        self.assertEqual(publish.call_args.args[1], release_tag('v0.1.0', self.platform))
        self.assertEqual(bind.call_args.args[1], 'core-published/v0.1.0/' + self.platform.replace('/', '-'))
        for name in ASSETS:
            self.assertEqual((self.output / name).read_bytes(), (self.fixture / name).read_bytes())

    def test_failed_verification_does_not_reserve_or_publish_stable(self):
        with patch('scripts.promote_core_release.verify_release', side_effect=ValueError('bad signer')), \
             patch('scripts.promote_core_release.bind') as bind, patch('scripts.promote_core_release.publish_release') as publish:
            with self.assertRaisesRegex(ValueError, 'bad signer'):
                self.execute()
        bind.assert_not_called()
        publish.assert_not_called()

    def test_modified_assets_are_rejected_before_verification(self):
        (self.fixture / 'CHANGELOG.md').write_text('Untrusted release notes')
        with patch('scripts.promote_core_release.verify_release') as verify, patch('scripts.promote_core_release.bind') as bind:
            with self.assertRaisesRegex(ValueError, 'notes or input'):
                self.execute()
        verify.assert_not_called()
        bind.assert_not_called()

    def test_newer_lower_target_wins_after_label_correction(self):
        self.add_rc('v0.2.0-rc.1', '2026-01-01T00:00:00Z')
        self.add_rc('v0.9.0-rc.1', '2026-01-03T00:00:00Z', previous='v0.0.1')
        with patch('scripts.promote_core_release.verify_release'), patch('scripts.promote_core_release.bind'), \
             patch('scripts.promote_core_release.publish_release') as publish:
            self.execute()
        self.assertIn('## v0.1.0\n', publish.call_args.args[3])

    def test_older_rc_rejected_even_when_target_is_higher(self):
        self.add_rc('v0.0.1-rc.1', '2026-01-03T00:00:00Z', run_id='124')
        with patch('scripts.promote_core_release.bind') as bind, patch('scripts.promote_core_release.publish_release') as publish:
            with self.assertRaisesRegex(ValueError, 'latest completed RC'):
                self.execute()
        bind.assert_not_called()
        publish.assert_not_called()

    def test_newer_source_wins_even_when_older_source_completes_later(self):
        older = 'c' * 40
        self.history.append(older)
        self.add_rc('v0.2.0-rc.1', '2026-01-03T00:00:00Z', run_id='124', source=older)
        with patch('scripts.promote_core_release.verify_release'), patch('scripts.promote_core_release.bind'), \
             patch('scripts.promote_core_release.publish_release') as publish:
            self.execute()
        self.assertIn('## v0.1.0\n', publish.call_args.args[3])

    def test_rejects_current_cycle_rc_off_first_parent_history(self):
        self.add_rc('v0.2.0-rc.1', '2026-01-03T00:00:00Z', source='c' * 40)
        with self.assertRaisesRegex(ValueError, 'first-parent'):
            self.execute()

    def test_same_second_uses_signing_run_as_tiebreaker(self):
        self.add_rc('v0.0.1-rc.1', '2026-01-02T00:00:00Z', run_id='124')
        with self.assertRaisesRegex(ValueError, 'latest completed RC'):
            self.execute()

    def test_changed_baseline_and_failed_run_are_rejected(self):
        for versions, conclusion in [([self.rc, 'v0.0.1'], 'success'), ([self.rc], 'failure')]:
            self.versions, self.run['conclusion'] = versions, conclusion
            with self.subTest(versions=versions, conclusion=conclusion), \
                 patch('scripts.promote_core_release.bind') as bind, patch('scripts.promote_core_release.publish_release') as publish:
                with self.assertRaises(ValueError):
                    self.execute()
                bind.assert_not_called()
                publish.assert_not_called()

    def test_untrusted_dispatch_and_non_rc_version_are_rejected(self):
        for candidate, event in [('v0.1.0', 'workflow_dispatch'), ('v1.0.0-rc.1', 'workflow_dispatch'), (self.rc, 'pull_request')]:
            with self.subTest(candidate=candidate, event=event), patch('scripts.promote_core_release.api') as api:
                with self.assertRaises(ValueError):
                    promote(candidate, 'linux/amd64', self.checkout, self.output, dict(self.env, GITHUB_EVENT_NAME=event))
                api.assert_not_called()


class PromoteArm64CoreReleaseTest(PromoteCoreReleaseTest):
    platform = 'linux/arm64'
