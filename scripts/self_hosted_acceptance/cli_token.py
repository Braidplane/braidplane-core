"""Mint a short-lived synthetic CLI PAT without exposing it in command output."""
import datetime
import json
from pathlib import Path
from frontend_security import client, base, login
import urllib.request

token = login()
expires = (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=1)).isoformat()
token_request = urllib.request.Request(
    base + '/api/users/profile/personal-access-tokens',
    data=json.dumps({'name': 'Synthetic CLI acceptance', 'expires_at': expires}).encode(),
    headers={'Content-Type': 'application/json', 'X-CSRF-Token': token},
)
with client.open(token_request, timeout=10) as response:
    assert response.status == 201
    credential = json.load(response)['token']
assert credential and '\n' not in credential
# This pre-created file is mounted writable only in the trusted fixture probe.
Path('/fixture/cli.env').write_text(
    'BRAIDPLANE_SERVER_URL=https://app.customer.test/api\n'
    'BRAIDPLANE_PAT=' + credential + '\n'
    'SSL_CERT_FILE=/fixture/ca.pem\n'
)
print('PASS: short-lived CLI credential written to protected fixture file')
