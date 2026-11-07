"""Verify unconfigured provider integrations fail locally without authorization URLs."""
import json
import secrets
import urllib.error
import urllib.request

from frontend_security import base, client, login

csrf = login()

def api(path, payload=None):
    req = urllib.request.Request(base + '/api' + path,
                                 data=json.dumps(payload).encode() if payload is not None else None,
                                 headers={'Content-Type': 'application/json', 'X-CSRF-Token': csrf},
                                 method='POST')
    try:
        response = client.open(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        body = json.load(response)
        return response.status, body

organization = 'optional-' + secrets.token_hex(6)
status, _ = api('/organizations', {'name': 'Synthetic Optional Providers', 'slug': organization})
assert status == 201, ('create synthetic organization', status)
root = '/organizations/' + organization
for provider, config in [('hubspot', {}), ('bigquery', {'project_id': 'synthetic-project'}), ('google_workspace', {})]:
    status, body = api(root + '/provider-connectors/' + provider + '/data-source-installs',
                       {'name': 'Synthetic ' + provider, 'slug': 'disabled-' + provider.replace('_', '-'),
                        'type': provider, 'config': config, 'read_only': True})
    assert status == 400, (provider, status)
    assert 'PROVIDER_NOT_CONFIGURED' in json.dumps(body), (provider, 'wrong rejection')
    assert 'authorization_url' not in body
    print('PASS: unconfigured ' + provider + ' rejected locally')
status, body = api(root + '/gitlab-connection/authorization', {'include_write_scopes': False})
assert status == 400, ('gitlab', status)
assert 'PROVIDER_NOT_CONFIGURED' in json.dumps(body), 'wrong GitLab rejection'
print('PASS: unconfigured gitlab rejected locally')
status, body = api(root + '/notification-connections', {'name': 'Synthetic Slack', 'slug': 'disabled-slack', 'provider': 'slack'})
if status == 201:
    status, body = api(root + '/notification-connections/disabled-slack/connect')
assert status == 400, ('slack', status)
assert 'PROVIDER_NOT_CONFIGURED' in json.dumps(body), 'wrong Slack rejection'
print('PASS: unconfigured slack rejected locally')
status, body = api(root + '/github-app-connection/install')
assert status == 422, ('github', status)
assert 'ORGANIZATION_UNAVAILABLE' in json.dumps(body), 'wrong GitHub rejection'
assert 'authorization_url' not in body
print('PASS: unconfigured GitHub App unavailable without an authorization URL')

with client.open(base + '/api' + root + '/data-sources', timeout=10) as response:
    sources = json.load(response)['data_sources']
    assert sources == [], 'failed provider installs must roll back pending data sources'
print('PASS: failed unconfigured source onboarding leaves no partial data sources')
