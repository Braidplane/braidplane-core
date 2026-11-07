import ssl,urllib.request,urllib.error,urllib.parse,http.cookiejar,http.cookies,re,json,time
from pathlib import Path
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*args):return None
jar=http.cookiejar.CookieJar()
client=urllib.request.build_opener(urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile='/fixture/ca.pem')),urllib.request.HTTPCookieProcessor(jar),NoRedirect())
base='https://app.customer.test'
def wait_ready():
    for url in ('http://api:3001/readyz', base + '/auth/login'):
        for attempt in range(30):
            try:
                with client.open(url, timeout=2) as response:
                    assert response.status == 200, 'fixture readiness must return 200'
                break
            except (urllib.error.URLError, OSError) as error:
                if isinstance(error, urllib.error.HTTPError):
                    error.close()
                if attempt == 29:
                    raise
                time.sleep(1)

def request(path,fields=None,expected=200):
    payload=urllib.parse.urlencode(fields).encode() if fields is not None else None
    req=urllib.request.Request(base+path,data=payload)
    for attempt in range(2):
        try:response=client.open(req,timeout=10)
        except urllib.error.HTTPError as error:response=error
        with response:
            body=response.read().decode()
            if response.status != 429 or expected == 429 or attempt:
                assert response.status==expected,(path,response.status,expected)
                return body,response.headers
        # The fixture keeps the default one-minute auth window; the server sends no Retry-After.
        time.sleep(60)
def login():
    page, _ = request('/auth/login')
    token = re.search(r'name="_csrf" value="([^"]+)"', page)
    assert token, 'CSRF token missing'
    passwords = json.loads(Path('/fixture/passwords.json').read_text())
    request('/auth/login', {'email': 'owner@customer.test', 'password': passwords['replacement'], '_csrf': token.group(1)}, 303)
    page, _ = request('/auth/login')
    token = re.search(r'name="_csrf" value="([^"]+)"', page)
    assert token, 'CSRF token missing'
    return token.group(1)

def check_security():
    page,headers=request('/auth/login')
    cookies=headers.get_all('Set-Cookie') or []
    assert cookies,'session cookie missing'
    for raw in cookies:
        parsed=http.cookies.SimpleCookie(raw)
        for cookie in parsed.values():
            assert cookie['secure'] and cookie['httponly'] and cookie['samesite'].lower()=='lax', ('cookie protections',bool(cookie['secure']),bool(cookie['httponly']),cookie['samesite'])
    token=re.search(r'name="_csrf" value="([^"]+)"',page)
    assert token,'CSRF token missing'
    passwords=json.loads(Path('/fixture/passwords.json').read_text())
    fields={'email':'owner@customer.test','password':passwords['replacement']}
    request('/auth/login',fields,403)
    request('/auth/login',fields|{'_csrf':'invalid-synthetic-token'},403)
    _,headers=request('/auth/login',fields|{'_csrf':token.group(1)},303)
    assert headers.get('Location','').startswith('/'), 'login redirect must stay local'
    profile,_=request('/api/users/profile')
    assert json.loads(profile)['slug']=='customer-owner','frontend session must authorize the profile API'
    print('PASS: secure HttpOnly SameSite cookies; missing/invalid CSRF rejected; local frontend login accepted with valid CSRF; shared-session profile authorized')

if __name__ == "__main__":
    check_security()
