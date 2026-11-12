# Google Workspace

Google Workspace uses normal connector dispatch without a deployment-wide
rollout flag. Configure its deployment-owned OAuth client and Picker settings
for onboarding. Complete Google's OAuth publication and verification process
for your deployment's provider app.

## Connection and consent

A Workspace source is a connected Google account and managed OAuth grant. Its
configuration records `access_mode: selected_files`, the only supported consent mode.
Resource targets belong in statements,
never source configuration. `read_only` defaults to true and Braidplane
enforces it independently of the Google grant.

Use provider onboarding to create an account-backed source. Static creation,
draft connection tests, static credentials, and source-level spreadsheet IDs are
unavailable. Reconnect or rotate a grant through an explicit Google consent flow. Failed or cancelled
consent preserves the prior grant.

Both read-only and write-enabled connections request `openid`, `email` and
`https://www.googleapis.com/auth/drive.file`. Google grants read/write capability
on files authorized to Braidplane or created by it. Braidplane blocks writes on
`read_only` connections. Spreadsheet authorization covers the whole selected
spreadsheet; the connector enforces the configured worksheet/range.

Use **Select files with Google** in the operation editor to authorize an existing
file or destination folder. Selection uses a separate short-lived browser GIS
token with only `drive.file`; it never exports the backend grant. The backend
checks file metadata with the connected account and the editor's source revision
before filling the target. Select with the connected Google account; a different
account's selection cannot make an inaccessible backend target usable. Scheduled
runs reuse the managed backend grant without opening Picker.

**Browse authorized files** and schema discovery list app-authorized/created
resources, not every file in the account. Selecting a folder authorizes that
folder, not its existing children. Authorize each existing source/update file.
New sink files created in an authorized folder are accessible to the app.
Authorization belongs to the Google app/account; statements remain the targets,
and selecting a file is not a tenant-local deny/allow list.

Register the OAuth callback and authorized JavaScript origin for the Workspace
web client. Enable Picker, Drive, Sheets, Docs and Slides APIs in its Google Cloud project.
Configure `picker_api_key` and `project_number` in the backend. Restrict the
browser key to Picker/Drive APIs and the intended application referrers plus
`https://docs.google.com/*`, as Google's current setup guide requires. The client,
key and project number must belong to the same project. These public browser
settings are returned only to members with data-source update permission.
See [Picker setup](https://developers.google.com/workspace/drive/picker/guides/web-picker-sample)
and [configuration](../../CONFIG.md#google-workspace).

`drive.file` is non-sensitive and avoids the restricted-scope assessment triggered
by account-wide Drive access. Basic app/branding verification and data-use,
security and deletion requirements remain. See [Google's scope guide](https://developers.google.com/workspace/drive/api/guides/api-specific-auth)
and rollout preparation.

### Existing all-files connections

Legacy `all_files`, broad scopes and mixed broad/per-file grants are rejected.
Existing records remain available for reconnect and inspection; no credentials,
content or grants are silently deleted or narrowed. Reauthorize deliberately with
selected-file access and authorize existing operation targets through Picker.
A consent flow cannot reliably narrow historical project grants: plan the owner's
Google revocation and any impact on other clients in the project separately.
Do not revoke the shared acceptance fixtures as part of deployment.

```sh
braidplane data-source provider onboard google_workspace --name workspace
braidplane data-source provider verify workspace --file-id file-one --file-id file-two
braidplane data-source provider files workspace --folder-id folder --drive-id drive --mime-type application/json
braidplane data-source provider worksheets workspace --spreadsheet-id spreadsheet
braidplane data-source provider metadata workspace --file-id file-one
```

`verify` sends file IDs and the current source revision, without storing a
source target. `files` returns one bounded page with optional folder, drive,
MIME type, and page-token filters. `worksheets` and `metadata` return safe
metadata only; they cannot authorize files or return credentials.

The data-source Operations tab lists saved queries; the query library owns
authoring and pagination. Connection management and Recheck access live in
Settings. The Schema tab displays a cached file inventory after an explicit
refresh, including names, stable IDs, and MIME types without reading contents.
Schema discovery returns at most the first 100 accessible files; use explicit
`list` operations and continuation tokens for additional pages.

## Statements

Workspace statements use `type: "google_workspace"`, a `resource`, and an
`operation`. Stable Google IDs are explicit statement fields.

| Resource | Operations | Contract |
|---|---|---|
| `spreadsheet` | `read`, `update` | Require `spreadsheet_id`, nonnegative `worksheet_id`, and a bounded title-free A1 range. `update` also requires ordered columns and exactly matching rows. It is one exact-range RAW write that preserves cells outside the rectangle. |
| `file` | `list` | Lists a bounded page by optional folder, drive, MIME type, and page token. |
| `file` | `read` | Requires `file_id` and `format` (`json`, `csv`, or `file`). |
| `file` | `create` | Requires `folder_id`, `name`, `format`, and `content`. Duplicate names are allowed; replay is `unsafe`. |
| `file` | `update` | Requires `file_id`, `format`, and `content`; changes content only. |

JSON reads and writes use an object or an array of row objects; CSV uses the
shared tabular serializer, and `file` preserves the native file payload.

Files support My Drive and shared drives. Creation does not upsert by name or
create native Google documents. Native document operations use the separate resources below; conversion remains unavailable. Writes have no automatic retry; a lost response
is ambiguous and requires explicit readback before replay.

Spreadsheet reads require a nonblank, unique string header for every selected
column in the first row of the range. Rejected headers return a public
explanation without cell contents or file identifiers. Reads preserve
strings/numbers/booleans and emit rows. Missing
cells are null. Updates accept literal row objects or `rows: "{{input}}"`;
they do not append, resize, create headers, or clear outside the rectangle.
Only identical fixed-coordinate spreadsheet updates may use
`naturally_idempotent`. File creation requires `unsafe` replay; file updates
require an explicit replay decision.

```json
{"type":"google_workspace","resource":"spreadsheet","operation":"read","spreadsheet_id":"book","worksheet_id":0,"range":"A1:C10"}
```

```json
{"type":"google_workspace","resource":"spreadsheet","operation":"update","spreadsheet_id":"book","worksheet_id":0,"range":"B2:C3","columns":["customer","amount"],"rows":"{{input}}"}
```

## Native Docs and Slides

Native operations use `resource: document` or `resource: presentation` with the
same selected-file grant. They request no additional scopes. Enable the Docs and
Slides APIs in the OAuth client's Google Cloud project before using them.

| Operation | Fields | Result |
|---|---|---|
| `read` | `file_id` | One row with `file_id`, `name`, `mime_type` and structured `content`. Docs includes every tab; Slides retains its presentation structure. |
| `create` | `folder_id`, `name` | A blank native file in the authorized folder; one row with its new stable `file_id`, name and MIME type. It does not consume pipeline input or copy a template. |
| `update` | `file_id`, `match_text`, string `content` | Replaces every exact, case-sensitive text match across the document/presentation and returns `occurrences_changed`, including zero when no match exists. Empty string removes the matches. |
| `export` | `file_id` | PDF file payload with the native source ID, filename, MIME type, size and SHA-256 digest. |

For pipeline text replacement, `content: "{{input}}"` resolves the existing
upstream input to text. String/CSV input stays literal; structured rows and JSON
values are serialized as JSON; binary file input is rejected. The connector does not infer a field or render a
template. Query literals remain literal, including placeholder-like text.
Reads and exports are sources; creates and updates are sinks. Native sinks
require `unsafe` replay, including saved operations. Server-side pipeline
validation resolves saved Workspace statements against the selected environment
bindings and checks their source/sink roles and replay policy without provider
calls. Replaying creation can
produce another file. Replaying replacement can change further matches if the
replacement or later edits introduce matching text. Each write has one provider
attempt; quota, transport or response failures after dispatch may be ambiguous.
Inspect the folder or stable file ID before choosing to replay.

The connector checks target MIME type and provider capabilities before dispatch.
Read-only Braidplane connections reject both native writes. PDF export respects
`canDownload`; text replacement respects `canEdit`; creation requires a writable
authorized folder. Native read uses the provider's content-read permissions.
Request/response bytes and execution time use the shared Workspace limits;
Google additionally limits Drive exports to 10 MB. No arbitrary provider batch
requests, scripting, layout/formatting edits, conversion, or sharing operations
are accepted. Blank Slides creation does not add slides or text shapes; update
works on existing text. These operations still require the separately authorized
disposable-provider acceptance before claiming hosted qualification.

```json
{"type":"google_workspace","resource":"document","operation":"update","file_id":"doc","match_text":"Customer name","content":"{{input}}"}
```

```json
{"type":"google_workspace","resource":"presentation","operation":"export","file_id":"deck"}
```

Provider contracts: [Docs read](https://developers.google.com/workspace/docs/api/reference/rest/v1/documents/get),
[Docs updates](https://developers.google.com/workspace/docs/api/reference/rest/v1/documents/batchUpdate),
[Slides updates](https://developers.google.com/workspace/slides/api/reference/rest/v1/presentations/batchUpdate),
[Drive export](https://developers.google.com/workspace/drive/api/reference/rest/v3/files/export).

## Proof and rollout

Workspace Proof is a grant-bound, metadata-only target operation. It can check
the selected target and worksheet/range bounds but never reads file contents,
exports data, or writes. The observation preview API accepts
`connector_type: google_workspace` with `resource: spreadsheet`,
`spreadsheet_id`, `worksheet_id`, and a title-free bounded A1 `range`.
For `resource: file`, `document`, or `presentation`, supply `file_id` and `operation` (`read`, `create`,
`update`, `export`, or `list`; export is native-only, list is file-only). For create/list, `file_id` identifies the target folder;
the observer checks its metadata and capabilities without listing children or
creating files. Unrelated selector fields are rejected. Live observations still
require the separate default-off `proof_observations.enabled` control and an
enabled environment policy.

The target must match the selected step's inline or saved operation. Source
claims accept read/list/export operations; destination claims accept update/create
operations. Checking a destination's capability performs metadata reads only.

Local tests, fixtures, and CLI responses do not prove
Google consent, hosted execution, or provider acceptance.

Keep the control disabled until authorized synthetic acceptance covers both
read-only and read/write grants, source and sink paths, read-only rejection, exact-range
preservation, revoked access, quota diagnostics, Proof metadata, and the
rollback window. See [Configuration](../../CONFIG.md).
