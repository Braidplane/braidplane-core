import json,re,time,urllib.request,urllib.error
from pathlib import Path
passwords=json.loads(Path('/fixture/passwords.json').read_text())
api='http://api:3001/api'
def request(path,payload=None,expected=200):
    data=json.dumps(payload).encode() if payload is not None else None
    req=urllib.request.Request(api+path,data=data,headers={'Content-Type':'application/json'})
    try:
        with urllib.request.urlopen(req,timeout=10) as response:
            status=response.status
            body=response.read()
    except urllib.error.HTTPError as error:
        status=error.code
        body=error.read()
    assert status==expected,(path,status,expected)
    return body

def messages():
    return json.load(urllib.request.urlopen('http://mail:8025/api/v1/messages',timeout=5))['messages']

# No output contains passwords, tokens, cookies or reset links.
request('/sessions',{'email':'owner@customer.test','password':passwords['initial']})
request('/users',{'full_name':'Closed Signup','email':'outsider@customer.test','password':passwords['initial']},403)
request('/auth/google',expected=503)
before=len(messages())
request('/auth/password/forgot',{'email':'missing@customer.test'},204)
assert len(messages())==before
request('/auth/password/forgot',{'email':'owner@customer.test'},204)
for _ in range(60):
    mailbox=messages()
    if len(mailbox)>before:break
    time.sleep(.5)
else:raise AssertionError('worker did not deliver reset email to customer SMTP')
message=json.load(urllib.request.urlopen('http://mail:8025/api/v1/message/'+mailbox[0]['ID'],timeout=5))
match=re.search(r'https://app\.customer\.test/auth/reset-password\?token=([a-zA-Z0-9]+)',message['Text'])
assert match,'reset email must use the customer application origin'
token=match.group(1)
request('/auth/password/reset',{'token':token,'password':passwords['replacement']},204)
request('/sessions',{'email':'owner@customer.test','password':passwords['initial']},401)
request('/sessions',{'email':'owner@customer.test','password':passwords['replacement']})
request('/auth/password/reset',{'token':token,'password':passwords['initial']},400)
print('PASS: local login; signup closed; Google disabled; generic reset request; customer-CA SMTP delivery; customer reset origin; password replacement; token single use')
