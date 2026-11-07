#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
python3.12 -m venv .worktree/docs-venv
python=.worktree/docs-venv/bin/python
"$python" -m pip install --require-hashes --only-binary=:all: -r requirements-docs.lock.txt
"$python" -m unittest scripts.test_check_docs
"$python" scripts/check_docs.py
PUBLIC_DOCS_ENVIRONMENT=staging \
PUBLIC_DOCS_SITE_URL=https://docs.example.test/ \
PUBLIC_DOCS_OPENAPI_URL=https://api.example.test/openapi.json \
    "$python" -m mkdocs build --strict --config-file mkdocs.public.yml --site-dir out/docs
