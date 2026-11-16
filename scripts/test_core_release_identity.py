import hashlib
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.core_release_identity import api, baseline, bind, execute, identity
from scripts.core_release_version import notes


class ReleaseIdentityTest(unittest.TestCase):
    def test_reservation_retry_and_refusal(self):
        stored = {}
        objects = {}
        def api(path, payload=None):
            if '/matching-refs/' in path:
                return [dict(ref='refs/tags/v0.1.0', object=stored['ref'])] if 'ref' in stored else []
            if path.endswith('/git/tags'):
                objects['tag'] = dict(payload, sha='tag-sha', object=dict(type=payload['type'], sha=payload['object']))
                return objects['tag']
            if path.endswith('/git/refs'):
                stored['ref'] = dict(type='tag', sha=payload['sha'])
                return {}
            if '/git/ref/' in path:
                return dict(object=stored['ref'])
            return objects['tag']
        with patch('scripts.core_release_identity.api', side_effect=api):
            bind('Example/core', 'v0.1.0', 'a' * 40, 'approved')
            bind('Example/core', 'v0.1.0', 'a' * 40, 'approved')
            with self.assertRaisesRegex(ValueError, 'identity differs'):
                bind('Example/core', 'v0.1.0', 'a' * 40, 'different artifact')
            with self.assertRaisesRegex(ValueError, 'source differs'):
                bind('Example/core', 'v0.1.0', 'b' * 40, 'approved')

    def test_failed_create_is_read_back_and_never_updated(self):
        error = subprocess.CalledProcessError(1, ['gh'])
        tag = dict(tag='v0.1.0', object=dict(type='commit', sha='a' * 40), message='approved')
        with patch('scripts.core_release_identity.api', side_effect=[[], dict(sha='tag'), error,
                   dict(object=dict(type='tag', sha='tag')), tag]) as api:
            bind('Example/core', 'v0.1.0', 'a' * 40, 'approved')
        self.assertEqual(api.call_count, 5)

    def test_failed_verification_never_marks_complete_and_retry_checks_same_identity(self):
        value = dict(repository='Example/core', source='a' * 40, record=dict(
            release=dict(version='v0.1.0', platform='linux/amd64'), migration=dict(version=1, digest='b' * 64)))
        env = dict(CORE_RELEASE_PUBLICATION_AUTHORIZED='yes', GITHUB_RUN_ID='123', GITHUB_RUN_ATTEMPT='2', DOCKER_CONFIG='/tmp')
        with patch('scripts.core_release_identity.identity', return_value=value), \
             patch('scripts.core_release_identity.bind') as bind_mock, \
             patch('scripts.core_release_identity.file_hash', return_value='c' * 64), \
             patch('scripts.core_release_identity.verify_release', side_effect=ValueError('bad signer')):
            with self.assertRaisesRegex(ValueError, 'bad signer'):
                execute('complete', Path('/tmp'), 'approved', Path('/tmp'), Path('/tmp/output'), env)
            self.assertEqual(bind_mock.call_count, 1)
        with patch('scripts.core_release_identity.identity', return_value=value), \
             patch('scripts.core_release_identity.bind') as bind_mock, \
             patch('scripts.core_release_identity.file_hash', return_value='c' * 64), \
             patch('scripts.core_release_identity.verify_release') as verify:
            execute('complete', Path('/tmp'), 'approved', Path('/tmp'), Path('/tmp/output'), env)
            self.assertEqual(bind_mock.call_args.args[1], 'core-published/v0.1.0/linux-amd64')
            self.assertEqual(verify.call_args.args[2]['attempt'], '2')

    def test_baseline_requires_rc_and_current_stable_reservation(self):
        def refs(*versions):
            return [dict(ref="refs/tags/" + version) for version in versions]
        plan = dict(previous="v0.2.3", version="v0.2.4-rc.2")
        with patch('scripts.core_release_identity.api', return_value=refs("v0.2.3", "v0.2.4-rc.1")):
            baseline("Example/core", plan)
        for history in (refs(), refs("v0.2.3", "v0.2.5")):
            with patch('scripts.core_release_identity.api', return_value=history), self.assertRaises(ValueError):
                baseline("Example/core", plan)
        with patch('scripts.core_release_identity.api', return_value=[]):
            baseline("Example/core", dict(previous="v0.0.0", version="v0.0.1-rc.1"))
        with patch('scripts.core_release_identity.api', return_value=[]), self.assertRaisesRegex(ValueError, 'requires a pre-v1 RC'):
            baseline("Example/core", dict(previous="v0.0.0", version="v0.0.1"))

    def test_ref_inventory_reads_all_pages(self):
        with patch('scripts.core_release_identity.subprocess.check_output', return_value=b'[[{"ref":"one"}],[{"ref":"two"}]]') as command:
            self.assertEqual(api('repos/Example/core/git/matching-refs/tags/v'), [dict(ref='one'), dict(ref='two')])
            self.assertEqual(command.call_args.args[0][-2:], ['--paginate', '--slurp'])

    def test_identity_binds_reviewed_materials_and_clean_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            plan = dict(previous="v0.1.0", version="v0.1.1", bump="patch", security=True,
                        supersedes=["v0.1.0"], changes=[dict(kind="fix", summary="Repair retries.")])
            files = {"release-plan.json": json.dumps(plan), "CHANGELOG.md": notes(plan),
                     "release-inputs.json": json.dumps(dict(through_sha="a" * 40)), "ci/core-release.json": "synthetic configuration",
                     "ci/toolchain/versions.env": "GO_VERSION=synthetic", ".github/workflows/core-release.yml": "synthetic workflow"}
            for name, contents in files.items():
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(contents)
            env = dict(GITHUB_REPOSITORY="Example/core", GITHUB_SHA="a" * 40, GITHUB_WORKFLOW_SHA="a" * 40,
                       GITHUB_WORKFLOW_REF="Example/core/.github/workflows/core-release.yml@refs/heads/main", GITHUB_RUN_ID="123")
            record = dict(release=dict(version="v0.1.1"))
            with patch('scripts.core_release_identity.validate_candidate', return_value=record), \
                 patch('scripts.core_release_identity.subprocess.check_output', side_effect=["a" * 40, b""]):
                result = identity(root, "approved", root, env)
            self.assertEqual(result['plan'], plan)
            self.assertEqual(result['record'], record)
            self.assertEqual(result['materials'], {name: hashlib.sha256(contents.encode()).hexdigest() for name, contents in files.items()})
            with patch('scripts.core_release_identity.validate_candidate', return_value=record), \
                 patch('scripts.core_release_identity.subprocess.check_output', side_effect=["a" * 40, b" M CHANGELOG.md"]):
                with self.assertRaisesRegex(ValueError, 'clean exact source'):
                    identity(root, "approved", root, env)
