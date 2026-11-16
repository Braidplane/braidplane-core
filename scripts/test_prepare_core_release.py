import json
import subprocess
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
        self.assertEqual(json.loads(metadata['release-plan.json'])['version'], 'v0.0.1-rc.1')
        self.assertEqual(json.loads(metadata['release-inputs.json'])['through_sha'], 'b' * 40)
        self.assertEqual(len(json.loads(metadata['release-inputs.json'])['pull_requests']), 2)
        self.assertFalse((self.root / 'release-plan.json').exists())

    def test_promotion_cursor_and_reserved_candidate_gaps(self):
        tag = dict(object=dict(type='commit', sha='c' * 40), message=json.dumps(dict(repository='Example/core', source='c' * 40,
                   promotion=dict(version='v0.1.0', candidate='v0.1.0-rc.2'),
                   record=dict(release=dict(version='v0.1.0-rc.2', core_revision='c' * 40)))))
        refs = [dict(ref='refs/tags/v0.1.1-rc.1'), dict(ref='refs/tags/v0.1.1-rc.3')]
        completed = [dict(ref='refs/tags/core-published/v0.1.0/linux-amd64', object=dict(type='tag', sha='tag'))]
        with patch('scripts.prepare_core_release.api', side_effect=[refs, completed, tag]), \
             patch('scripts.prepare_core_release.collect', return_value=[pull()]) as collect_mock:
            metadata = release_metadata(self.root, 'Example/core', 'b' * 40)
        self.assertEqual(collect_mock.call_args.args[2], 'c' * 40)
        plan = json.loads(metadata['release-plan.json'])
        self.assertEqual(plan['version'], 'v0.1.1-rc.4')
        self.assertEqual(plan['previous'], 'v0.1.0')
        self.assertEqual(plan['supersedes'], [])

    def test_completed_rc_deduplicates_docs_and_aggregates_changes(self):
        first = pull()
        saved = dict(number=1, title=first['title'], merge_sha=first['merge_commit_sha'], labels=['release:patch'])
        tag = dict(object=dict(type='commit', sha='b' * 40), message=json.dumps(dict(repository='Example/core', source='b' * 40,
                   inputs=dict(since_sha='a' * 40, pull_requests=[saved]),
                   record=dict(release=dict(version='v0.0.1-rc.1', core_revision='b' * 40)))))
        refs = [dict(ref='refs/tags/v0.0.1-rc.1')]
        completed = [dict(ref='refs/tags/core-published/v0.0.1-rc.1/linux-amd64', object=dict(type='tag', sha='tag'))]
        with patch('scripts.prepare_core_release.api', side_effect=[refs, completed, tag]), \
             patch('scripts.prepare_core_release.collect', return_value=[first, pull(2, 'release:none')]), \
             patch('scripts.prepare_core_release.git', return_value='d' * 40 + '\n' + 'b' * 40):
            self.assertIsNone(release_metadata(self.root, 'Example/core', 'd' * 40))
        with patch('scripts.prepare_core_release.api', side_effect=[refs, completed]), \
             patch('scripts.prepare_core_release.collect', return_value=[first, pull(2, 'release:minor'), pull(3, 'release:breaking')]):
            metadata = release_metadata(self.root, 'Example/core', 'd' * 40)
        plan = json.loads(metadata['release-plan.json'])
        self.assertEqual(plan['version'], 'v0.1.0-rc.1')
        self.assertEqual([change['kind'] for change in plan['changes']], ['fix', 'feature', 'breaking'])

    def test_legacy_stable_cursor_and_new_patch_candidate(self):
        stable = dict(ref='refs/tags/core-published/v0.1.0/linux-amd64', object=dict(type='tag', sha='stable'))
        candidate = dict(ref='refs/tags/core-published/v0.1.1-rc.1/linux-amd64', object=dict(type='tag', sha='candidate'))
        stable_tag = dict(object=dict(type='commit', sha='c' * 40), message=json.dumps(dict(repository='Example/core', source='c' * 40,
                          record=dict(release=dict(version='v0.1.0', core_revision='c' * 40)))))
        first = pull()
        saved = dict(number=1, title=first['title'], merge_sha=first['merge_commit_sha'], labels=['release:patch'])
        rc_tag = dict(object=dict(type='commit', sha='b' * 40), message=json.dumps(dict(repository='Example/core', source='b' * 40,
                      inputs=dict(since_sha='c' * 40, pull_requests=[saved]),
                      record=dict(release=dict(version='v0.1.1-rc.1', core_revision='b' * 40)))))
        with patch('scripts.prepare_core_release.api', side_effect=[[dict(ref='refs/tags/v0.1.1-rc.1')], [stable, candidate], stable_tag, rc_tag]), \
             patch('scripts.prepare_core_release.collect', return_value=[first, pull(2)]) as collect_mock, \
             patch('scripts.prepare_core_release.git', return_value='d' * 40 + '\n' + 'b' * 40):
            metadata = release_metadata(self.root, 'Example/core', 'd' * 40)
        self.assertEqual(collect_mock.call_args.args[2], 'c' * 40)
        plan = json.loads(metadata['release-plan.json'])
        self.assertEqual(plan['version'], 'v0.1.1-rc.2')
        self.assertEqual(len(plan['changes']), 2)

    def test_out_of_order_candidates_and_older_queued_source(self):
        first, second = pull(), pull(2)
        saved = lambda p: dict(number=p['number'], title=p['title'], merge_sha=p['merge_commit_sha'], labels=['release:patch'])
        def candidate(version, source, pulls):
            return dict(object=dict(type='commit', sha=source), message=json.dumps(dict(
                repository='Example/core', source=source, inputs=dict(since_sha='a' * 40, pull_requests=[saved(p) for p in pulls]),
                record=dict(release=dict(version=version, core_revision=source)))))
        refs = [dict(ref='refs/tags/v0.0.1-rc.1'), dict(ref='refs/tags/v0.0.1-rc.2')]
        completed = [dict(ref=f'refs/tags/core-published/v0.0.1-rc.{n}/linux-amd64', object=dict(type='tag', sha=str(n))) for n in (1, 2)]
        newer = candidate('v0.0.1-rc.1', 'c' * 40, [first, second])
        older = candidate('v0.0.1-rc.2', 'b' * 40, [first])
        with patch('scripts.prepare_core_release.api', side_effect=[refs, completed, newer, older]), \
             patch('scripts.prepare_core_release.collect', return_value=[first, second, pull(3, 'release:none')]), \
             patch('scripts.prepare_core_release.git', return_value='d' * 40 + '\n' + 'c' * 40 + '\n' + 'b' * 40):
            self.assertIsNone(release_metadata(self.root, 'Example/core', 'd' * 40))
        with patch('scripts.prepare_core_release.api', side_effect=[refs, completed, newer]), \
             patch('scripts.prepare_core_release.collect', return_value=[first]), \
             patch('scripts.prepare_core_release.git', return_value='b' * 40), \
             patch('scripts.prepare_core_release.subprocess.run') as run:
            self.assertIsNone(release_metadata(self.root, 'Example/core', 'b' * 40))
            self.assertEqual(run.call_args.args[0][-2:], ['b' * 40, 'c' * 40])
        with patch('scripts.prepare_core_release.api', side_effect=[refs, completed, newer]), \
             patch('scripts.prepare_core_release.collect', return_value=[first]), \
             patch('scripts.prepare_core_release.git', return_value='b' * 40), \
             patch('scripts.prepare_core_release.subprocess.run', side_effect=subprocess.CalledProcessError(1, 'git')):
            with self.assertRaisesRegex(ValueError, 'outside candidate history'):
                release_metadata(self.root, 'Example/core', 'b' * 40)

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
