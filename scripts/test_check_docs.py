"""Synthetic regressions for documentation gate failures."""

import json
from pathlib import Path
import tempfile
import unittest

from scripts.check_docs import check


class CheckDocsTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.write("docker/docs/inventory.json", json.dumps({"ignored_directories": ["out"], "contextual_markdown": {"README.md": "Repository entrypoint"}}))
        self.write("mkdocs.public.yml", "docs_dir: docs-public\nnav:\n  - Home: index.md\n")
        self.write("docs-public/index.md", "# Home\n[Reference][ref]\n\n[ref]: #home\n")
        self.write("doc/index.md", "# Engineering\n[Guide](guide.md)\n")
        self.write("doc/guide.md", "# Guide\n[Home](../README.md#hello-world)\n")
        self.write("README.md", "# Hello world\n")

    def write(self, path, content):
        file = self.root / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(content)

    def test_valid_and_ignored_code_examples(self):
        self.write("doc/guide.md", "# Guide\n```md\n[Not a link](missing.md)\n```\n")
        self.write("out/generated.md", "[Ignored](absent.md)")
        self.assertEqual([], check(self.root))

    def test_missing_reference_image_and_html_targets(self):
        for content in ("[Broken][ref]\n\n[ref]: missing.md", "![Image](missing.png)", '<a href="missing.md">link</a>'):
            with self.subTest(content=content):
                self.write("doc/guide.md", content)
                self.assertTrue(any("missing/outside target" in e for e in check(self.root)))

    def test_missing_anchor(self):
        self.write("README.md", "# Renamed\n")
        self.assertTrue(any("missing anchor" in e for e in check(self.root)))

    def test_orphans_in_both_roots(self):
        for base in ("doc", "docs-public"):
            self.write(f"{base}/orphan.md", "# Orphan")
        self.assertEqual(2, sum("orphan " in e for e in check(self.root)))

    def test_duplicate_nav(self):
        self.write("mkdocs.public.yml", "docs_dir: docs-public\nnav:\n  - Home: index.md\n  - Again: ./index.md\n")
        self.assertTrue(any("duplicate target" in e for e in check(self.root)))

    def test_public_root_isolation(self):
        self.write("docs-public/index.md", "[Internal](../doc/guide.md)")
        self.assertTrue(any("escapes docs-public" in e for e in check(self.root)))

    def test_missing_nav_input(self):
        (self.root / "docs-public/index.md").unlink()
        self.assertTrue(any("missing/outside Markdown" in e for e in check(self.root)))

    def test_unclassified_and_stale_exclusions(self):
        self.write("NEW.md", "# New")
        (self.root / "README.md").unlink()
        errors = check(self.root)
        self.assertTrue(any("unclassified" in e for e in errors))
        self.assertTrue(any("stale or unexplained" in e for e in errors))
