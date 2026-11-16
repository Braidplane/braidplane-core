import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.prepare_core_release import classified, collect, prepare, release_metadata, release_plan


def pull(number=1, label="release:patch"):
    return dict(number=number, title="Synthetic repair", labels=[dict(name=label)],
                merge_commit_sha="b" * 40, merged_at="2026-01-01T00:00:00Z",
                base=dict(ref="main", repo=dict(full_name="Example/core")))


class PrepareCoreReleaseTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / 'ci').mkdir()
        (self.root / 'ci/core-release-start.json').write_text(json.dumps(dict(source='a' * 40)))
        self.config = dict(platform='linux/amd64', edge='example.test/edge@sha256:' + 'c' * 64, postgres='example.test/postgres@sha256:' + 'd' * 64)
        (self.root / 'ci/core-release.json').write_text(json.dumps(self.config))
        (self.root / 'CHANGELOG.md').write_text('# Changelog\n\n## Unreleased\n')
        self.env = dict(GITHUB_REPOSITORY='Example/core', GITHUB_SHA='b' * 40, GITHUB_WORKFLOW_SHA='b' * 40,
                        GITHUB_REF='refs/heads/main', GITHUB_EVENT_NAME='workflow_run', RUNNER_ENVIRONMENT='github-hosted',
                        GITHUB_WORKFLOW_REF='Example/core/.github/workflows/core-release-plan.yml@refs/heads/main')
        self.event = dict(workflow_run=dict(id=123, run_attempt=1, head_sha='b' * 40))

    def test_labels_select_patch_explicit_minor_no_release_and_security(self):
        self.assertEqual(release_plan('v0.1.2', [pull()])['version'], 'v0.1.3')
        for label in ('release:minor', 'release:breaking'):
            self.assertEqual(release_plan('v0.1.2', [pull(label=label)])['version'], 'v0.2.0')
        self.assertIsNone(release_plan('v0.1.2', [pull(label='release:none')]))
        self.assertIsNone(release_plan('v0.1.2', []))
        security = pull()
        security['labels'].append(dict(name='release:security'))
        self.assertEqual(release_plan('v0.1.2', [security])['supersedes'], ['v0.1.2'])

    def test_refuses_missing_conflicting_or_unknown_classification(self):
        for labels in ([], ['release:patch', 'release:minor'], ['release:major'], ['release:none', 'release:security']):
            candidate = pull()
            candidate['labels'] = [dict(name=label) for label in labels]
            with self.subTest(labels=labels), self.assertRaises(ValueError):
                classified([candidate])

    def test_metadata_covers_all_pending_changes_and_exact_source(self):
        with patch('scripts.prepare_core_release.api', side_effect=[[], []]), \
             patch('scripts.prepare_core_release.collect', return_value=[pull(), pull(2, 'release:none')]):
            metadata = release_metadata(self.root, 'Example/core', 'b' * 40)
        self.assertEqual(json.loads(metadata['release-plan.json'])['version'], 'v0.0.1')
        self.assertEqual(json.loads(metadata['release-inputs.json'])['through_sha'], 'b' * 40)
        self.assertEqual(len(json.loads(metadata['release-inputs.json'])['pull_requests']), 2)
        self.assertFalse((self.root / 'release-plan.json').exists())

    def test_published_cursor_and_abandoned_version_preserve_unreleased_changes(self):
        tag = dict(object=dict(type='commit', sha='c' * 40), message=json.dumps(dict(repository='Example/core', source='c' * 40,
                   record=dict(release=dict(version='v0.1.0', core_revision='c' * 40)))))
        refs = [dict(ref='refs/tags/v0.1.1')]
        completed = [dict(ref='refs/tags/core-published/v0.1.0/linux-amd64', object=dict(type='tag', sha='tag'))]
        with patch('scripts.prepare_core_release.api', side_effect=[refs, completed, tag]), \
             patch('scripts.prepare_core_release.collect', return_value=[pull()]) as collect_mock:
            metadata = release_metadata(self.root, 'Example/core', 'b' * 40)
        self.assertEqual(collect_mock.call_args.args[2], 'c' * 40)
        plan = json.loads(metadata['release-plan.json'])
        self.assertEqual(plan['version'], 'v0.1.2')
        self.assertEqual(plan['supersedes'], ['v0.1.1'])

    def test_collect_deduplicates_rebased_commits_and_refuses_direct_commits(self):
        with patch('scripts.prepare_core_release.subprocess.run'), \
             patch('scripts.prepare_core_release.git', return_value='a' * 40 + '\n' + 'b' * 40), \
             patch('scripts.prepare_core_release.pages', return_value=[pull()]):
            self.assertEqual(collect('Example/core', self.root, 'c' * 40, 'b' * 40), [pull()])
        with patch('scripts.prepare_core_release.subprocess.run'), \
             patch('scripts.prepare_core_release.git', return_value='b' * 40), \
             patch('scripts.prepare_core_release.pages', return_value=[]):
            with self.assertRaisesRegex(ValueError, 'direct commits'):
                collect('Example/core', self.root, 'c' * 40, 'b' * 40)

    def test_dispatch_requires_ci_and_never_writes_source_or_prs(self):
        metadata = {'release-plan.json': json.dumps(dict(version='v0.0.1'))}
        with patch('scripts.prepare_core_release.api', side_effect=[dict(object=dict(sha='b' * 40)), dict(workflow_runs=[]), None]) as api, \
             patch('scripts.prepare_core_release.admit') as admit, \
             patch('scripts.prepare_core_release.git', side_effect=['b' * 40, '']), \
             patch('scripts.prepare_core_release.release_metadata', return_value=metadata):
            prepare(self.root, self.env, self.event)
        admit.assert_called_once_with('Example/core', 'b' * 40, '123', '1')
        self.assertTrue(api.call_args.args[0].endswith('/core-release.yml/dispatches'))
        self.assertEqual(api.call_args.args[1]['inputs'], dict(self.config, ci_run_id='123', ci_attempt='1'))

    def test_failed_ci_untrusted_event_and_duplicate_run_do_not_dispatch(self):
        for event in ('pull_request', 'push'):
            with patch('scripts.prepare_core_release.api') as api, self.assertRaises(ValueError):
                prepare(self.root, dict(self.env, GITHUB_EVENT_NAME=event), self.event)
            api.assert_not_called()
        with patch('scripts.prepare_core_release.api', return_value=dict(object=dict(sha='b' * 40))) as api, \
             patch('scripts.prepare_core_release.admit', side_effect=ValueError('failed CI')):
            with self.assertRaisesRegex(ValueError, 'failed CI'):
                prepare(self.root, self.env, self.event)
            self.assertEqual(api.call_count, 1)
        with patch('scripts.prepare_core_release.api', side_effect=[dict(object=dict(sha='b' * 40)), dict(workflow_runs=[dict(head_sha='b' * 40)])]) as api, \
             patch('scripts.prepare_core_release.admit'), patch('scripts.prepare_core_release.git', side_effect=['b' * 40, '']), \
             patch('scripts.prepare_core_release.release_metadata', return_value={'release-plan.json': '{}'}):
            prepare(self.root, self.env, self.event)
        self.assertEqual(api.call_count, 2)
