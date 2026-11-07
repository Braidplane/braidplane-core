"""Exercise local authentication, PAT issuance and worker SMTP without changing the password."""
import json
import runpy
import time
import urllib.request

runpy.run_path('/fixture/cli_token.py')
runpy.run_path('/fixture/providers.py')

def messages():
    with urllib.request.urlopen('http://mail:8025/api/v1/messages', timeout=5) as response:
        return json.load(response)['messages']

before = len(messages())
request = urllib.request.Request('http://api:3001/api/auth/password/forgot',
                                 data=json.dumps({'email': 'owner@customer.test'}).encode(),
                                 headers={'Content-Type': 'application/json'})
with urllib.request.urlopen(request, timeout=10) as response:
    assert response.status == 204
for _ in range(60):
    if len(messages()) > before:
        print('PASS: traced authentication/PAT and worker SMTP traffic exercised')
        break
    time.sleep(.5)
else:
    raise AssertionError('worker did not deliver the traced reset email')
