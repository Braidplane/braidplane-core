"""Validate repository Markdown targets and the isolated public navigation."""

from collections import Counter
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit
import json
import os
import sys

import markdown
import yaml


EXTENSIONS = ["admonition", "pymdownx.details", "pymdownx.superfences", "tables", "toc"]


class Links(HTMLParser):
    def __init__(self, html):
        super().__init__(convert_charrefs=True)
        self.targets = []
        self.anchors = set()
        self.feed(html)

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        for name in (("id", "name") if tag == "a" else ("id",)):
            if attrs.get(name):
                self.anchors.add(attrs[name])
        for name in ("href", "src"):
            if attrs.get(name):
                self.targets.append(attrs[name])


def document(path):
    source = path.read_text(encoding="utf-8").replace("{{ public_openapi_url }}", "https://api.example.test/openapi.json")
    return Links(markdown.markdown(source, extensions=EXTENSIONS))


def nav_targets(nav):
    if isinstance(nav, str):
        yield nav
    elif isinstance(nav, list):
        for item in nav:
            yield from nav_targets(item)
    elif isinstance(nav, dict):
        for item in nav.values():
            yield from nav_targets(item)


def check(root):
    root = root.resolve()
    policy = json.loads((root / "docker/docs/inventory.json").read_text())
    paths = []
    for directory, dirs, files in os.walk(root):
        dirs[:] = [d for d in dirs if d not in policy["ignored_directories"]]
        paths.extend(Path(directory) / f for f in files if f.endswith(".md"))
    documents = {p: document(p) for p in paths}
    errors = []
    for source, doc in documents.items():
        for target in doc.targets:
            url = urlsplit(target)
            if url.scheme or url.netloc:
                continue
            dest = (source.parent / unquote(url.path)).resolve() if url.path else source
            if not dest.is_relative_to(root) or not dest.is_file():
                errors.append(f"{source.relative_to(root)}: missing/outside target {target}")
                continue
            if source.is_relative_to(root / "docs-public") and not dest.is_relative_to(root / "docs-public"):
                errors.append(f"{source.relative_to(root)}: public link escapes docs-public: {target}")
            if url.fragment and dest in documents and unquote(url.fragment) not in documents[dest].anchors:
                errors.append(f"{source.relative_to(root)}: missing anchor {target}")

    config = yaml.safe_load((root / "mkdocs.public.yml").read_text())
    if config["docs_dir"] != "docs-public":
        errors.append("public docs_dir must remain docs-public")
    public = list(nav_targets(config["nav"]))
    internal = [urlsplit(t).path for t in documents[root / "doc/index.md"].targets if not urlsplit(t).scheme and not urlsplit(t).netloc]
    for label, base, targets in (("public nav", root / "docs-public", public), ("engineering index", root / "doc", internal)):
        resolved = [(base / unquote(t)).resolve() for t in targets]
        for target, count in Counter(resolved).items():
            if count > 1:
                errors.append(f"{label}: duplicate target {target.relative_to(root)}")
            if not target.is_relative_to(base) or target not in documents:
                errors.append(f"{label}: missing/outside Markdown target {target}")
        expected = {p for p in documents if p.is_relative_to(base)}
        if label == "engineering index":
            expected.discard(root / "doc/index.md")
        for path in expected - set(resolved):
            errors.append(f"{label}: orphan {path.relative_to(root)}")

    contextual = {str(p.relative_to(root)) for p in documents if not p.is_relative_to(root / "doc") and not p.is_relative_to(root / "docs-public")}
    excluded = policy["contextual_markdown"]
    for path in contextual - excluded.keys():
        errors.append(f"unclassified contextual Markdown: {path}")
    for path, reason in excluded.items():
        if path not in contextual or not reason.strip():
            errors.append(f"stale or unexplained contextual exclusion: {path}")
    return errors


if __name__ == "__main__":
    errors = check(Path(__file__).resolve().parents[1])
    if errors:
        print("\n".join(errors), file=sys.stderr)
        sys.exit(1)
    print("Documentation inventory, local links and Markdown anchors passed")
