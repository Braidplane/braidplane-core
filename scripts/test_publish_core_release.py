import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.publish_core_release import publish_candidate, publish_release, release_tag
from scripts.validate_core_candidate import file_hash


class PublishCoreReleaseTest(unittest.TestCase):
    def test_platform_release_tags_preserve_amd64_downloads(self):
        for version in ('v0.1.0-rc.1', 'v0.1.0'):
            self.assertEqual(release_tag(version, 'linux/amd64'), version)
            self.assertEqual(release_tag(version, 'linux/arm64'), version + '-linux-arm64')
        with self.assertRaises(ValueError):
            release_tag('v0.1.0', 'linux/386')

    def test_candidate_requires_exact_completed_rc_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            value = dict(repository='Example/core', source='a' * 40, plan=dict(version='v0.1.0-rc.1', security=False, supersedes=[], changes=[]),
                         record=dict(release=dict(version='v0.1.0-rc.1', platform='linux/amd64')))
            (root / 'identity.json').write_text(json.dumps(value))
            env = dict(GITHUB_SHA='a' * 40, GITHUB_REPOSITORY='Example/core')
            for message, valid in [(json.dumps(value), True), ('{}', False)]:
                with self.subTest(valid=valid), patch('scripts.publish_core_release.api', side_effect=[
                        dict(object=dict(sha='tag')), dict(message=message)]), \
                     patch('scripts.publish_core_release.publish_release') as publish:
                    if valid:
                        publish_candidate(root, root, env)
                        self.assertTrue(publish.call_args.args[-1])
                    else:
                        with self.assertRaisesRegex(ValueError, 'completion identity differs'):
                            publish_candidate(root, root, env)
                        publish.assert_not_called()

    def test_draft_upload_then_publish_and_exact_retry(self):
        with tempfile.TemporaryDirectory() as tmp:
            asset = Path(tmp) / 'record.json'
            asset.write_text('synthetic')
            release = dict(id=7, tag_name='v0.1.0-rc.1', body='notes', prerelease=True, draft=True)
            with patch('scripts.publish_core_release.subprocess.check_output', return_value=b'[[]]'), \
                 patch('scripts.publish_core_release.api', side_effect=[release, [], {}]) as api, \
                 patch('scripts.publish_core_release.subprocess.run') as upload:
                publish_release('Example/core', release['tag_name'], 'a' * 40, 'notes', [asset], True)
            self.assertNotIn('--clobber', upload.call_args.args[0])
            self.assertEqual(api.call_args.args[1], dict(draft=False, make_latest='false'))
            release['draft'] = False
            with patch('scripts.publish_core_release.subprocess.check_output', return_value=json.dumps([[release]]).encode()), \
                 patch('scripts.publish_core_release.api', return_value=[dict(name=asset.name, digest='sha256:' + file_hash(asset))]), \
                 patch('scripts.publish_core_release.subprocess.run') as upload:
                publish_release('Example/core', release['tag_name'], 'a' * 40, 'notes', [asset], True)
            upload.assert_not_called()

    def test_existing_metadata_or_asset_mismatch_is_never_overwritten(self):
        with tempfile.TemporaryDirectory() as tmp:
            asset = Path(tmp) / 'record.json'
            asset.write_text('synthetic')
            release = dict(id=7, tag_name='v0.1.0', body='notes', prerelease=False, draft=False)
            for body, assets in [('changed notes', []), ('notes', []), ('notes', [dict(name=asset.name, digest='sha256:bad')])]:
                with self.subTest(body=body, assets=assets), \
                     patch('scripts.publish_core_release.subprocess.check_output', return_value=json.dumps([[release]]).encode()), \
                     patch('scripts.publish_core_release.api', return_value=assets), \
                     patch('scripts.publish_core_release.subprocess.run') as upload:
                    with self.assertRaises(ValueError):
                        publish_release('Example/core', 'v0.1.0', 'a' * 40, body, [asset], False)
                    upload.assert_not_called()

    def test_upload_failure_leaves_release_unpublished(self):
        release = dict(id=7, tag_name='v0.1.0', body='notes', prerelease=False, draft=True)
        with patch('scripts.publish_core_release.subprocess.check_output', return_value=b'[[]]'), \
             patch('scripts.publish_core_release.api', side_effect=[release, []]) as api, \
             patch('scripts.publish_core_release.subprocess.run', side_effect=subprocess.CalledProcessError(1, ['gh'])):
            with self.assertRaises(subprocess.CalledProcessError):
                publish_release('Example/core', 'v0.1.0', 'a' * 40, 'notes', [Path('/tmp/synthetic')], False)
        self.assertEqual(api.call_count, 2)
