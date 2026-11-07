"""Verify opt-in Community signup needs no legal policy or consent metadata."""
import json
import secrets
import urllib.request
from pathlib import Path

from frontend_security import request, wait_ready

wait_ready()
page, _ = request('/auth/signup')
for field in ('terms_document_version', 'terms_accepted'):
    assert 'name="' + field + '"' not in page, 'Community signup must not require legal consent'
payload = {'full_name': 'Synthetic Community Signup',
           'email': 'signup-' + secrets.token_hex(6) + '@customer.test',
           'password': json.loads(Path('/fixture/passwords.json').read_text())['initial']}
req = urllib.request.Request('http://api:3001/api/users', data=json.dumps(payload).encode(),
                             headers={'Content-Type': 'application/json'})
with urllib.request.urlopen(req, timeout=10) as response:
    assert response.status == 201, 'Community account creation without agreement metadata must succeed'
print('PASS: opt-in Community signup form and API require no legal consent or document configuration')
