#!/usr/bin/env python3
"""Fail bruno-all when any checked-in request was not actually executed."""
import json
import sys
from pathlib import Path


def requests(root):
    return {p.resolve() for p in root.rglob('*.bru')
            if p.name not in {'collection.bru', 'folder.bru'}
            and 'environments' not in p.relative_to(root).parts}


def executed(reports):
    seen, rejected = set(), set()
    for report in reports.glob('*.json'):
        for iteration in json.loads(report.read_text()):
            for result in iteration['results']:
                path = Path(result['suitename'] + '.bru').resolve()
                if (result.get('skipped') or result.get('status') != 'pass'
                        or not isinstance((result.get('response') or {}).get('status'), int)):
                    rejected.add(path)
                else:
                    seen.add(path)
    return seen, rejected


def check(root, reports, ci=False):
    expected = requests(root)
    if ci:
        live = (root / 'github-pipelines/lifecycle.bru').resolve()
        if live in expected:
            expected.remove(live)
            print('Deferred to explicit live staging acceptance: github-pipelines/lifecycle.bru')
    seen, rejected = executed(reports)
    missing = (expected - seen) | rejected
    if missing:
        print('Bruno requests missing, skipped or failed:', file=sys.stderr)
        for path in sorted(missing):
            print(f'  {path.relative_to(root.resolve())}', file=sys.stderr)
        return 1
    scope = 'candidate-local ' if ci else ''
    print(f'Bruno coverage: all {len(expected)} {scope}request files executed (no omissions).')
    return 0


if __name__ == '__main__':
    if len(sys.argv) not in (3, 4) or (len(sys.argv) == 4 and sys.argv[3] != '--ci'):
        sys.exit('usage: bruno-coverage.py ROOT REPORTS [--ci]')
    sys.exit(check(Path(sys.argv[1]), Path(sys.argv[2]), ci=len(sys.argv) == 4))
