# Disposable self-hosted acceptance tests

Use this fixture to test Community with fresh customer-owned keys, a customer CA
and separate database roles. It runs the control worker, SMTP and a TLS frontend,
with registration closed and Google disabled. The containers use an internal
Docker network with no published ports.

Prerequisites: Docker Compose v2, Python 3.12+ and OpenSSL. Run from the repository
root. Build images from the checkout you want to test:

```sh
docker build --target backend -t braidplane-selfhost-acceptance-backend:local .
docker build --target migrate -t braidplane-selfhost-acceptance-migrate:local .
docker build --target frontend -t braidplane-selfhost-acceptance-frontend:local .
docker build --target cli -t braidplane-selfhost-acceptance-cli:local .
python3 scripts/self_hosted_acceptance/prepare.py .worktree/self-hosted-acceptance
```

Preparation writes random role and account passwords, independent application
and envelope keys, and a short-lived CA and server certificate to protected
files. It stops if the target directory already exists, including after a partial
failure. Keep a failed fixture for diagnosis and choose a new directory for the
next run. Keep its contents out of Git and its secret files out of command output.

Run one fixture at a time under the default name, or use Compose's
`--project-name` override for independent runs. Use a disposable database for
these commands and keep retained data separate.

```sh
fixture() { docker compose -f .worktree/self-hosted-acceptance/compose.yml "$@"; }
fixture config --quiet
fixture up -d --wait database mail
fixture run --rm migrate
fixture run --rm grants
fixture run --rm -T bootstrap < .worktree/self-hosted-acceptance/bootstrap-password
fixture up -d api worker frontend edge
fixture run --rm probe python /fixture/network.py
fixture run --rm probe python /fixture/recovery.py
fixture run --rm probe python /fixture/frontend_security.py
fixture run --rm probe python /fixture/cli_token.py
python3 scripts/self_hosted_acceptance/check_cli.py .worktree/self-hosted-acceptance
```

The CLI token probe writes a one-hour synthetic PAT to the protected `cli.env`
file. The CLI reads the token from its container environment without saving login
configuration. The trust check must succeed with the customer CA and fail
without it.

The recovery probe changes the generated account password. Run it once per
fresh fixture; the frontend-security probe uses the replacement password. Probes
print status summaries and keep passwords, reset tokens and cookies out of their
output. Investigate failures in the fixture logs while keeping certificate
checks, CSRF and role permissions intact. Save exact command output and image
IDs in ignored storage if you need to retain the test results.

Consecutive auth probes can reach the default five-attempt, one-minute throttle.
The shared frontend probe waits for that window and retries a 429 once. A second
429 fails the probe. Application limits stay enabled.

The network probe checks application readiness and confirms that one public TCP
connection fails from the internal network. Capture other process, browser and
CLI traffic separately. The recovery probe sends real HTTP requests through the
database queue, worker and SMTP delivery path. The frontend-security probe uses
an HTTPS client with a cookie jar. Use the browser check below to test JavaScript,
and check private-source grants and executor behavior separately.

When you have saved the results, remove this fixture's containers and synthetic
database volume:

```sh
fixture down --volumes
```

If you plan to resume the same run, keep its generated credentials and CA with
the database. Reuse those keys with the existing application state. These checks
leave production SMTP, DNS, system trust stores and provider accounts untouched.

Preparation checks are part of `task test`, or can run independently:

```sh
python3 -m unittest scripts.test_self_hosted_acceptance
```

After checking that registration is closed by default, use the temporary overlay
to enable signup and create a synthetic account:

```sh
signup_fixture() { docker compose -f .worktree/self-hosted-acceptance/compose.yml -f .worktree/self-hosted-acceptance/signup.compose.yml "$@"; }
signup_fixture up -d api worker frontend
signup_fixture run --rm probe python /fixture/signup.py
fixture up -d api worker frontend
fixture run --rm probe python /fixture/closed.py
```

The probe renders the signup form and sends a name, email and password to the
account-creation API. Both signup probes wait for the API and frontend to become
ready, with at most 30 attempts per endpoint and a two-second request timeout.
Restore the base Compose file to close registration again, then check that the
final probe passes.

Compile the private PostgreSQL integration test for the fixture image's
architecture, then run it inside the internal network:

```sh
fixture_arch=$(docker image inspect braidplane-selfhost-acceptance-backend:local --format '{{.Architecture}}')
GOOS=linux GOARCH="$fixture_arch" CGO_ENABLED=0 bash scripts/go-toolchain.sh test -c \
  -o .worktree/self-hosted-acceptance/private-source.test ./backend/e2e
fixture run --rm private-source
fixture run --rm -e SELF_HOSTED_EXPECT_UNTRUSTED_CA=true \
  -e SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt private-source
```

The test pins the fixture's only customer-DNS IPv4 answer and loads the normal
configuration with a grant for one organization and source. It uses the real
connector factory with verified TLS, then queries PostgreSQL to confirm TLS is
active. Connections for another organization or source, a wrong port or a changed
IP pin must fail. The second run must reject the untrusted customer CA. The
broad private-host override stays disabled. This checks the connector's direct
connection; complete pipelines and executor isolation need their own tests.

To test the browser flow and record page-request origins, install the pinned
frontend tooling and build the browser runner:

```sh
task frontend-install
docker build -f scripts/self_hosted_acceptance/Dockerfile.browser \
  -t braidplane-selfhost-acceptance-browser:local .
fixture run --rm browser
```

The pinned Playwright image matches the repository's browser tooling. The NSS
utility imports the generated fixture CA into that container's browser trust
store, leaving the host trust store unchanged. TLS checks stay enabled.
Chromium tests local login, hidden Google and signup actions when unconfigured,
protected cookies and authenticated profile access. The probe records origins
only. It aborts requests to an unexpected origin and fails the test. Results
exclude passwords, cookies, page dumps and full request URLs. Coverage is limited
to this browser flow; other application features, browser background services
and server/CLI socket traffic need separate capture.

Build the tracer from the current local binaries to record API, worker and CLI
socket destinations. It traces only `connect`. Reads, writes, request bodies,
environment variables and packet payloads stay out of the trace. The temporary
SYS_PTRACE capability applies within each fixture container's PID namespace.
The tracer uses neither the host PID namespace nor the host network namespace.

```sh
docker build -f scripts/self_hosted_acceptance/Dockerfile.trace \
  -t braidplane-selfhost-acceptance-trace:local .
trace_fixture() { docker compose -f .worktree/self-hosted-acceptance/compose.yml -f .worktree/self-hosted-acceptance/trace.compose.yml "$@"; }
trace_fixture up -d api worker
fixture up -d --force-recreate frontend
trace_fixture run --rm probe python /fixture/traffic.py
trace_fixture logs --no-color api > .worktree/self-hosted-acceptance/api-connect.log
trace_fixture logs --no-color worker > .worktree/self-hosted-acceptance/worker-connect.log
trace_fixture run --rm cli > /dev/null 2> .worktree/self-hosted-acceptance/cli-connect.log
python3 scripts/self_hosted_acceptance/check_connect.py .worktree/self-hosted-acceptance
fixture up -d api worker
fixture up -d --force-recreate frontend
fixture run --rm probe python /fixture/network.py
```

The checker requires connections to the expected database, SMTP and TLS edge
destinations. It permits Docker DNS and rejects other parsed Internet socket
destinations or unknown address formats. Save the traces before restoring normal
images, including after a failed check. Confirm that the restored API and worker
have no added tracing capability.

Recreate the frontend after each API image switch so its proxy reconnects to the
replacement container rather than retaining the previous upstream connection.

These traces cover startup and the tested authentication, reset and CLI calls.
They omit unconnected datagrams, IPC and features outside the test. The internal
Docker network blocks public routes independently of tracing.

Docker retains two 10 MiB stdout log files per application container. The
application's optional file-log rotation has its own limits. Set equivalent
retention in the container runtime or service manager for a real installation,
so stdout logs cannot fill the host's storage.

You can also test unconfigured providers on their own. The trace sequence above
includes these checks:

```sh
fixture run --rm probe python /fixture/providers.py
```

The probe creates a synthetic organization and checks that HubSpot, BigQuery,
Workspace, GitLab, Slack and GitHub App onboarding fail locally when unconfigured.
It also checks that failed source installs leave no partial data sources. It
uses no provider accounts and sends no live provider requests.
