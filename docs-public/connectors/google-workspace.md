# Google Workspace

Read and update selected Sheets ranges and Drive files, or use bounded native
Docs/Slides operations. Authorize files through Google Picker, then use the
operations below. See [support levels and evidence](support-levels.md) for
connector status and provider requirements.

## Connect and authorize

The deployment operator configures a Google OAuth web client and Picker settings
and enables the Drive, Sheets, Docs and Slides APIs. Connect Google Workspace in
data sources or run:

```sh
braidplane data-source provider onboard google_workspace --name workspace
```

Static credentials, static data-source creation and unsaved draft connection
tests are unavailable. The managed grant uses `drive.file`, `openid` and `email`.
Braidplane connections default to read-only; provider permission alone cannot
enable writes. Reconnect through explicit consent after revocation or scope
changes. Testing-mode grants expire after seven days.

Choose **Select files with Google** in the operation editor using the connected
account. A selected folder does not grant access to its existing children;
authorize each existing source/update file. New files created by the app in an
authorized folder are accessible. Discovery lists app-authorized resources,
not the whole account; cached schema discovery is limited to the first 100 files.
Explicit file listing supports bounded pages and continuation tokens.

## Operations and payloads

| Resource | Operations | Scope |
|---|---|---|
| `spreadsheet` | `read`, `update` | Explicit spreadsheet ID, worksheet ID and bounded A1 range. Read normalizes header-based values to rows. Update writes one exact RAW rectangle with ordered columns and matching rows. |
| `file` | `list`, `read`, `create`, `update` | Drive JSON/CSV/file bytes. Create requires an authorized folder; update requires the exact file ID and changes content only. |
| `document`, `presentation` | `read`, `create`, `update`, `export` | Structured native content, blank native creation, case-sensitive text replacement, or PDF file export. |

Example source statement, using an already-authorized synthetic spreadsheet:

```json
{"type":"google_workspace","resource":"spreadsheet","operation":"read","spreadsheet_id":"book","worksheet_id":0,"range":"A1:C10"}
```

Reads require unique nonblank string column headers in the first selected row.
An update requires `columns` and `rows`, optionally `rows: "{{input}}"`; it does
not append, resize or clear cells outside the target rectangle.
Native text updates require `file_id`, `match_text` and string `content`.
They replace every exact case-sensitive match; zero matches is a valid result.
Native create needs `folder_id` and `name` and creates a blank file, not a
template copy or rendered pipeline input.

## Delivery and limits

Workspace sources use full reads; incremental checkpoints and CDC are unsupported.
Each write has one provider attempt. Native Docs/Slides writes and file creation
require `unsafe` replay. Only identical fixed-coordinate Sheets updates may use
`naturally_idempotent`. Inspect remote state after an ambiguous failure before
replaying; creation can duplicate files and text replacement can change further
matches. Provider edit/download permissions still apply.

Shared execution time, row and byte limits apply. Drive PDF exports also have a
10 MB provider limit. Layout/formatting edits, template copying, conversion,
arbitrary provider batches, sharing administration and scripting are unsupported.
Proof checks target metadata only; it neither reads file content nor executes
a write. The customer owns target selection and business mappings; Braidplane
owns the implemented native adapter and managed token-refresh behavior.
