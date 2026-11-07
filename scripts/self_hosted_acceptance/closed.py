"""Verify public signup is closed after restoring baseline configuration."""
import json
import urllib.error
import urllib.request

from frontend_security import wait_ready

wait_ready()
request = urllib.request.Request(
    'http://api:3001/api/users',
    data=json.dumps({'full_name': 'Synthetic Closed Signup', 'email': 'closed@customer.test',
                     'password': 'synthetic-never-created-password'}).encode(),
    headers={'Content-Type': 'application/json'},
)
try:
    response = urllib.request.urlopen(request, timeout=10)
except urllib.error.HTTPError as error:
    response = error
with response:
    assert response.status == 403, 'public signup must remain closed'
print('PASS: closed registration restored')
