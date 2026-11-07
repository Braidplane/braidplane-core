"""Assert Community entitlement behavior in the disposable self-hosted fixture."""
import argparse
import http.cookies
import json
import urllib.error
import urllib.request
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--fixture', type=Path, required=True)
parser.add_argument('--create', action='store_true')
args = parser.parse_args()
base = 'http://api:3001/api'
password = json.loads((args.fixture / 'passwords.json').read_text())['initial']
cookie = ''
csrf = ''
def request(path, payload=None, expected=200, authenticated=True, method=None):
    headers = {'Content-Type': 'application/json'}
    if authenticated and cookie:
        headers['Cookie'] = cookie
    if csrf and authenticated:
        headers['X-CSRF-Token'] = csrf
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        response = urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        body = response.read()
        assert response.status == expected, (path, response.status, expected)
        return (json.loads(body) if 'json' in response.headers.get('Content-Type', '') else body), response.headers

_, headers = request('/sessions', {'email': 'owner@customer.test', 'password': password})
parsed = http.cookies.SimpleCookie()
for value in headers.get_all('Set-Cookie', []):
    parsed.load(value)
cookie = '; '.join(key + '=' + value.value for key, value in parsed.items())
assert cookie, 'login must establish a session'
_, headers = request('/users/profile')
csrf = headers.get('X-CSRF-Token', '')
for value in headers.get_all('Set-Cookie', []):
    parsed.load(value)
cookie = '; '.join(key + '=' + value.value for key, value in parsed.items())
assert csrf, 'authenticated read must supply CSRF token'
if args.create:
    request('/organizations', {'name': 'Community acceptance', 'slug': 'community-acceptance'}, 201)
org, _ = request('/organizations/community-acceptance')
assert 'entitlement' not in org, 'Community organization response must not expose a commercial plan'
request('/organizations/community-acceptance', {'name': 'Community acceptance'}, expected=204, method='PUT')
request('/organizations/community-acceptance/proof-configurations')
export, headers = request('/organizations/community-acceptance/security-audit-events/export')
assert 'text/csv' in headers.get('Content-Type', ''), 'audit export must return CSV'
assert export, 'audit export must contain its header'
request('/organizations/community-acceptance', expected=401, authenticated=False)
request('/organizations/community-acceptance/proof-configurations', expected=401, authenticated=False)
request('/organizations/community-acceptance/security-audit-events/export', expected=401, authenticated=False)
print('PASS: authenticated Community access; Community-only organization diagnostics; organization writes, Proof configurations and audit export; anonymous access denied')
