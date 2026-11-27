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
load semantics, pagination, authentication, renewal and recovery for this CLI
build. Configure provider access using the connector's setup guide.

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
contact the server. Server validation can save validation failures or a contract
impact report. Use run history to inspect execution and delivery.

Use one YAML document or JSON value per pipeline file. Trailing whitespace is
allowed in either format, as are trailing comments in YAML. If the file contains
another document, value, or invalid trailing text, `pipeline validate` exits
nonzero and reports the parse error on stderr before contacting the server.
`pipeline format` sends the full file to the API and prints the formatted result
on success.

### Compare a candidate with a deployed version

```sh
braidplane pipeline diff users --file candidate.pipeline.yaml --org acme --project data --baseline upstream
braidplane pipeline diff users --git-commit "$PR_HEAD_SHA" --git-path pipelines/users.pipeline.yaml \
  --org acme --project data --baseline deployed --environment dev
```

Upstream is the default baseline. It compares against the project's current
pipeline. A candidate already imported upstream can show no changes even when
`dev` runs an older version.

For PR checks, use the full head commit ID and the repository-relative pipeline
path. Run the command inside a local Git repository containing that commit.
The CLI reads the committed file and includes its commit and path in the result.
Use `--file` for edits before committing.

The deployed comparison requires an explicit environment. It reports the observed
deployment revision, the full immutable baseline spec hash, the candidate spec
hash, and semantic findings with a severity. Breaking changes make the CLI exit
nonzero. An unavailable deployment or version returns an error for the selected
scope. If a deployment changes during comparison, the result still identifies
the revision observed at lookup time.

API clients can send the candidate as a JSON pipeline spec to
`POST /organizations/{slug}/projects/{project_slug}/environments/{environment_slug}/pipelines/{pipeline_slug}/deployment:compare`.
The route requires pipeline read access. It compares authored specs and omits
spec values from its findings. It does not resolve environment bindings, test
live sources, execute the pipeline, or record a Proof result.

### Inspect pipeline runs

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

## Pause and drain a pipeline

Open **Pause and drain** from the pipeline page with the affected environment
selected. The API exposes the same controls through GET and PUT on
`/organizations/{slug}/projects/{project_slug}/environments/{environment_slug}/pipelines/{pipeline_slug}/execution-control`
under the API base URL. Inspect requires pipeline read access. Pause and resume
also require pipeline update access to the selected target.

1. Inspect the target and use its current revision to pause. For example, use
   revision `0` for a target that has never changed execution state.

   ```sh
   braidplane pipeline execution status orders --org acme --project etl --environment prod
   braidplane pipeline execution pause orders --org acme --project etl --environment prod --expected-revision 0
   ```

2. Refresh status until the active count reaches zero while paused. Held attempts
   have not started and can remain queued after active attempts drain. Manual
   runs, backfills, and retained-input recovery return a conflict while paused.
   Scheduled, verified webhook, completion, and automatic retry arrivals can
   queue on the existing trigger policies. Webhook retention and retry deadlines
   continue to apply while work waits. Older trigger jobs first appear in status
   when a worker records their intake.
3. Inspect the run links and write evidence. A completed write recorded in the
   effect ledger still stands. An uncertain outcome can include a write that
   reached the provider before the worker recorded confirmation. Verify the
   provider's state before retrying. An old or missing worker observation is
   insufficient to declare an attempt drained. If a synchronous recovery loses
   its process and has no recorded queue job, automatic worker reconciliation
   cannot settle its ownership. The attempt remains active in this view. Use run
   history for attempts that finished before the pause.
4. After correcting the issue, inspect status and use its current revision to
   resume. Workers recheck held jobs after 30 seconds and use the existing
   queue to pick up eligible attempts. A revision conflict requires a fresh
   inspection before you submit again.

   ```sh
   braidplane pipeline execution resume orders --org acme --project etl --environment prod --expected-revision 1
   ```

The pause holds across restarts and deployment changes. Resuming does not create
new attempts for existing jobs. Each environment and pipeline has its own
control, independent of other tenants.
