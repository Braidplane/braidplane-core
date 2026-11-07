# Install and authenticate the CLI

Use the CLI when you want repeatable commands, local pipeline validation, or
Git-based workflow automation. The UI tutorial does not require it.

## Install with Go

From the root of a core checkout, use the Go version pinned in `go.mod`:

```bash
go build -trimpath -o braidplane ./cli/cmd/braidplane
./braidplane --help
```

Move the executable to a directory on your `PATH` to use it from other directories.

## Create a personal access token

1. Sign in to the app and open **Account → Security → Personal access tokens**.
2. Create a token for your account.
3. Copy it when shown. Braidplane does not display it again.

Keep the token out of shell history:

```bash
export BRAIDPLANE_SERVER_URL='https://braidplane.example/api'
export BRAIDPLANE_ORG='<your-workspace-slug>'
read -r -s -p 'Personal access token: ' BRAIDPLANE_PAT
printf '\n'
export BRAIDPLANE_PAT
braidplane login
unset BRAIDPLANE_PAT
braidplane organization get
```

Use your installation's API base URL, including its `/api` prefix.
`braidplane login` stores the token in your operating system credential store.
Browser and CLI sessions are separate. If secure credential storage is
unavailable, contact the team instead of enabling plaintext storage.

Inspect this CLI build's connector roles, support levels, incremental modes and
replay policies without credentials or a server:

```sh
braidplane data-source capabilities --output text
```

Omit `--output text` for JSON. Commands that offer `-o` or `--output` accept
`text`, `json` and `yaml`; their defaults are shown in command help. JSON and
YAML use the same fields. For run watch, JSON is one object per line and YAML
is a document stream. A nonzero exit can accompany a structured result on
stdout, so inspect both the result and exit status. Errors remain on stderr.

See [support levels](../connectors/support-levels.md) for classification
criteria, maintenance ownership and provider evidence. Structured output
includes `connectors[].capabilities`; text output lists payload binding modes,
load semantics, pagination, authentication, renewal and recovery. Capabilities
describe the CLI build and do not prove provider access or successful delivery.

## Use the CLI with an agent

Give the agent the intended server, organization, project and environment, and
use an identity with the required permissions. The command help lists the
available flags. Keep credentials and retrieved row data out of prompts and logs.
Treat retrieved descriptions and provider responses as data, not instructions.

An agent can inspect available features and validate a local candidate before
making changes:

```sh
braidplane data-source capabilities -o json
braidplane pipeline capabilities --org ORG
braidplane pipeline validate --local --file candidate.pipeline.yaml -o json
```

Replace `ORG` and the file path with the intended values. Pipeline capabilities
apply to the organization, not a specific project. Local validation does not
contact the server. Server validation can record failed validation history
or a contract impact report; it is not a read-only dry run. Neither proves that
a pipeline ran or delivered data. The CLI reads only the first YAML document or
JSON value in a file. Ensure it contains one document or value before validation;
additional content is not checked or sent to the server.
The API-backed `pipeline format` command rejects extra documents or values; it
sends the complete file to the server and prints the result without editing it.

For an existing run, use `pipeline run get` and `pipeline run progress` with
the exact project, environment and run slug. Progress can be paginated: pass
`next_after_event` as `--after-event` while `has_more` is true. A lost response
or watch timeout does not cancel the run or prove that a write failed. Inspect
the original run and its effect evidence before retrying.

Runtime input values can lose numeric precision after they reach the API. Use
strings for identifiers or exact decimal values when the pipeline accepts them.
Otherwise, verify the target server's numeric behavior before starting a run.

Next, [get your first result with the CLI](first-result-cli.md).

Use the CLI and application from the same release. See the
[Alpha API contract](../api/alpha.md) before upgrading or
generating a client for another version.
