import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from scripts.prepare_core_release import BRANCH_PREFIX, FILES, check_branch, classified, collect, commit_message, create_pr, prepare, release_plan, serialized
from scripts.core_release_version import notes


def pull(number=1, label="release:patch", **extra):
    return dict(number=number, title="Synthetic repair", labels=[dict(name=label)],
                merge_commit_sha="b" * 40, merged_at="2026-01-01T00:00:00Z",
                base=dict(ref="main", repo=dict(full_name="Example/core")),
                head=dict(ref="synthetic", repo=dict(full_name="Example/core"), sha="b" * 40), **extra)


class PrepareCoreReleaseTest(unittest.TestCase):
    def test_labels_select_patch_explicit_minor_and_no_release(self):
        self.assertEqual(release_plan("v0.1.2", [pull()])["version"], "v0.1.3")
        for label in ("release:minor", "release:breaking"):
            plan = release_plan("v0.1.2", [pull(label=label)])
            self.assertEqual(plan["version"], "v0.2.0")
        self.assertIsNone(release_plan("v0.1.2", [pull(label="release:none")]))
        self.assertIsNone(release_plan("v0.1.2", []))
        self.assertEqual(release_plan("v0.0.0", [pull()])["version"], "v0.0.1")

    def test_classification_refuses_missing_conflicting_unknown_and_multiline(self):
        for labels in ([], ["release:patch", "release:minor"], ["release:major"]):
            candidate = pull()
            candidate["labels"] = [dict(name=label) for label in labels]
            with self.subTest(labels=labels), self.assertRaises(ValueError):
                classified([candidate])
        candidate = pull()
        candidate["title"] = "Bad\nheading"
        with self.assertRaises(ValueError):
            classified([candidate])

    def test_batch_uses_highest_bump_and_security_relationship(self):
        security = pull(number=2, label="release:breaking")
        security["labels"].append(dict(name="release:security"))
        plan = release_plan("v0.1.2", [pull(), security])
        self.assertEqual(plan["version"], "v0.2.0")
        self.assertEqual(len(plan["changes"]), 2)
        self.assertTrue(plan["security"])
        self.assertEqual(plan["supersedes"], ["v0.1.2"])
        security["labels"] = [dict(name="release:none"), dict(name="release:security")]
        with self.assertRaises(ValueError):
            classified([security])

    def test_collect_deduplicates_rebased_commits_and_rejects_direct_commits(self):
        candidate = pull()
        with patch('scripts.prepare_core_release.subprocess.run') as ancestry, \
             patch('scripts.prepare_core_release.git', return_value="a" * 40 + "\n" + "b" * 40), \
             patch('scripts.prepare_core_release.pages', return_value=[candidate]):
            self.assertEqual(collect("Example/core", Path('/tmp'), "c" * 40, "b" * 40), [candidate])
            ancestry.assert_called_once()
        with patch('scripts.prepare_core_release.subprocess.run'), \
             patch('scripts.prepare_core_release.git', return_value="b" * 40), \
             patch('scripts.prepare_core_release.pages', return_value=[]):
            with self.assertRaisesRegex(ValueError, 'direct commits'):
                collect("Example/core", Path('/tmp'), "c" * 40, "b" * 40)

    def test_generated_release_pr_cannot_hide_extra_changed_files(self):
        candidate = pull()
        candidate['head']['ref'] = BRANCH_PREFIX + 'v0.1.0'
        for files in (FILES, FILES | {'runtime.py'}):
            with patch('scripts.prepare_core_release.subprocess.run'), \
                 patch('scripts.prepare_core_release.git', return_value="b" * 40), \
                 patch('scripts.prepare_core_release.pages', side_effect=[[candidate], [dict(filename=f) for f in files]]):
                if files == FILES:
                    self.assertEqual(collect("Example/core", Path('/tmp'), "c" * 40, "b" * 40), [])
                else:
                    with self.assertRaisesRegex(ValueError, 'unexpected files'):
                        collect("Example/core", Path('/tmp'), "c" * 40, "b" * 40)

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / 'ci').mkdir()
        (self.root / 'ci/core-release-start.json').write_text(json.dumps(dict(source='a' * 40)))
        (self.root / 'CHANGELOG.md').write_text('# Changelog\n\n## Unreleased\n\n- Pending\n')
        self.env = dict(GITHUB_REPOSITORY='Example/core', GITHUB_SHA='b' * 40, GITHUB_WORKFLOW_SHA='b' * 40,
                        GITHUB_REF='refs/heads/main', GITHUB_EVENT_NAME='push', RUNNER_ENVIRONMENT='github-hosted',
                        GITHUB_WORKFLOW_REF='Example/core/.github/workflows/core-release-plan.yml@refs/heads/main')

    def test_prepare_rejects_untrusted_context_before_any_writes(self):
        for key, value in (('GITHUB_REF', 'refs/pull/1/merge'), ('GITHUB_WORKFLOW_SHA', 'c' * 40),
                           ('GITHUB_EVENT_NAME', 'pull_request'), ('RUNNER_ENVIRONMENT', 'self-hosted')):
            env = dict(self.env, **{key: value})
            with patch('scripts.prepare_core_release.api') as api, self.assertRaises(ValueError):
                prepare(self.root, env)
            api.assert_not_called()

    def test_prepare_from_bootstrap_and_queued_snapshot(self):
        for ledger in (None, dict(through_sha='c' * 40)):
            if ledger:
                (self.root / 'release-inputs.json').write_text(json.dumps(ledger))
            with patch('scripts.prepare_core_release.git', side_effect=['b' * 40, '']), \
                 patch('scripts.prepare_core_release.api', side_effect=[dict(object=dict(sha='b' * 40)), []]), \
                 patch('scripts.prepare_core_release.pages', return_value=[]), \
                 patch('scripts.prepare_core_release.collect', return_value=[pull()]) as collect_mock, \
                 patch('scripts.prepare_core_release.create_pr', return_value=dict(html_url='synthetic')) as create:
                prepare(self.root, self.env)
            self.assertEqual(collect_mock.call_args.args[2], 'c' * 40 if ledger else 'a' * 40)
            self.assertEqual(create.call_args.args[2]['version'], 'v0.0.1')
            self.assertEqual(create.call_args.args[3]['through_sha'], 'b' * 40)

    def test_open_release_pr_refreshes_from_all_pending_changes(self):
        pending = pull()
        pending['head']['ref'] = BRANCH_PREFIX + 'snapshot'
        with patch('scripts.prepare_core_release.git', side_effect=['b' * 40, '']), \
             patch('scripts.prepare_core_release.api', side_effect=[dict(object=dict(sha='b' * 40)), []]), \
             patch('scripts.prepare_core_release.pages', return_value=[pending]), \
             patch('scripts.prepare_core_release.check_branch'), \
             patch('scripts.prepare_core_release.collect', return_value=[pull(), pull(number=2, label='release:minor')]), \
             patch('scripts.prepare_core_release.create_pr', return_value=dict(html_url='synthetic')) as create:
            prepare(self.root, self.env)
        self.assertEqual(create.call_args.args[2]['version'], 'v0.1.0')
        self.assertEqual(create.call_args.args[-1], pending)

    def test_stale_merged_plan_is_reprepared_including_intervening_changes(self):
        plan = release_plan('v0.0.0', [pull()])
        (self.root / 'release-plan.json').write_text(json.dumps(plan))
        (self.root / 'release-inputs.json').write_text(json.dumps(dict(since_sha='a' * 40, through_sha='c' * 40)))
        (self.root / 'CHANGELOG.md').write_text('# Changelog\n\n## Unreleased\n\n' + notes(plan))
        with patch('scripts.prepare_core_release.git', side_effect=['b' * 40, '']), \
             patch('scripts.prepare_core_release.api', side_effect=[dict(object=dict(sha='b' * 40)), []]), \
             patch('scripts.prepare_core_release.pages', return_value=[]), \
             patch('scripts.prepare_core_release.collect', return_value=[pull(), pull(number=2, label='release:minor')]) as collect_mock, \
             patch('scripts.prepare_core_release.create_pr', return_value=dict(html_url='synthetic')) as create:
            prepare(self.root, self.env)
        self.assertEqual(collect_mock.call_args.args[2], 'a' * 40)
        self.assertEqual(create.call_args.args[2]['version'], 'v0.1.0')
        self.assertNotIn('## v0.0.1', create.call_args.args[4])

    def test_real_git_first_parent_cursor_limits_discovery(self):
        def git(*args):
            return subprocess.check_output(['git', '-C', str(self.root), *args], text=True).strip()
        git('init', '-q')
        git('config', 'commit.gpgsign', 'false')
        git('config', 'user.name', 'Synthetic')
        git('config', 'user.email', 'synthetic@example.test')
        git('add', '.')
        git('commit', '-qm', 'initial')
        base = git('rev-parse', 'HEAD')
        (self.root / 'synthetic.txt').write_text('synthetic')
        git('add', '.')
        git('commit', '-qm', 'squashed synthetic PR')
        source = git('rev-parse', 'HEAD')
        candidate = pull()
        candidate['merge_commit_sha'] = source
        with patch('scripts.prepare_core_release.pages', return_value=[candidate]) as pages:
            self.assertEqual(collect('Example/core', self.root, base, source), [candidate])
        self.assertIn(source, pages.call_args.args[0])

    def test_branch_refresh_fast_forwards_without_force_and_preserves_concurrent_edits(self):
        plan = release_plan('v0.1.0', [pull()])
        inputs = dict(through_sha='b' * 40, pull_requests=[])
        existing = pull()
        existing['head']['ref'] = BRANCH_PREFIX + 'snapshot'
        existing['head']['sha'] = 'old-head'
        calls = []
        def api(path, payload=None, method="POST"):
            calls.append((path, payload, method))
            if path.endswith('/git/commits/' + 'b' * 40):
                return dict(tree=dict(sha='base-tree'))
            if path.endswith('/git/trees'):
                self.assertEqual({f['path'] for f in payload['tree']}, FILES)
                return dict(sha='tree')
            if path.endswith('/git/commits/old-head'):
                return dict(sha='old-head', tree=dict(sha='old-tree'))
            if path.endswith('/git/commits'):
                self.assertEqual(payload['parents'], ['old-head', 'b' * 40])
                return dict(sha='new')
            if '/git/refs/heads/' in path:
                self.assertEqual(payload, dict(sha='new', force=False))
                self.assertEqual(method, 'PATCH')
                raise subprocess.CalledProcessError(422, ['gh'])
            return {}
        with patch('scripts.prepare_core_release.api', side_effect=api), \
             patch('scripts.prepare_core_release.ensure_ci') as ci:
            with self.assertRaises(subprocess.CalledProcessError):
                create_pr('Example/core', 'b' * 40, plan, inputs, notes(plan), existing)
        ci.assert_not_called()

    def test_branch_edits_are_preserved(self):
        plan = release_plan('v0.1.0', [pull()])
        inputs = dict(since_sha='a' * 40, through_sha='b' * 40,
                      pull_requests=[dict(number=1, title='Synthetic repair', labels=['release:patch'])])
        files = serialized(plan, inputs, notes(plan))
        message = commit_message(plan, files)
        def api(path):
            return dict(message=message) if '/git/commits/' in path else dict(files=[dict(filename=f) for f in FILES])
        with patch('scripts.prepare_core_release.contents', side_effect=lambda repo, sha, name: files[name]), \
             patch('scripts.prepare_core_release.subprocess.run'), \
             patch('scripts.prepare_core_release.api', side_effect=api):
            check_branch('Example/core', self.root, 'head')
            files['release-inputs.json'] += ' '
            with self.assertRaisesRegex(ValueError, 'reviewer edits'):
                check_branch('Example/core', self.root, 'head')

    def test_interrupted_creation_reuses_identical_branch_without_rewriting_it(self):
        plan = release_plan('v0.1.0', [pull()])
        inputs = dict(through_sha='b' * 40)
        calls = []
        def api(path, payload=None):
            calls.append((path, payload))
            if path.endswith('/git/commits/' + 'b' * 40):
                return dict(tree=dict(sha='base-tree'))
            if path.endswith('/git/trees'):
                return dict(sha='tree')
            if '/matching-refs/' in path:
                branch = path.split('/heads/')[1]
                return [dict(ref='refs/heads/' + branch, object=dict(sha='existing'))]
            if path.endswith('/git/commits/existing'):
                return dict(sha='existing', tree=dict(sha='tree'))
            return {}
        with patch('scripts.prepare_core_release.api', side_effect=api), \
             patch('scripts.prepare_core_release.pages', return_value=[]), \
             patch('scripts.prepare_core_release.publish_pr', return_value=dict(number=123)) as publish:
            self.assertEqual(create_pr('Example/core', 'b' * 40, plan, inputs, notes(plan)), dict(number=123))
        self.assertEqual(publish.call_args.args[2], 'existing')
        self.assertFalse(any('/git/refs' in path for path, _ in calls))


if __name__ == '__main__':
    unittest.main()

