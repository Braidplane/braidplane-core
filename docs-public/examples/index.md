# Pipeline examples

Choose a task, then use its canonical recipe to set up, validate, deploy, run and inspect a pipeline with synthetic data.

| Task | Flow | Recipe |
| --- | --- | --- |
| Enrich API orders while keeping unknown customers | HTTP + PostgreSQL → S3 JSON | [Customer enrichment](http-customer-enrichment.md) |
| Reconcile payments, refunds and unmatched ledger entries | S3 CSV + PostgreSQL → BigQuery | [Settlement reconciliation](settlement-ledger-reconciliation.md) |
| Apply spreadsheet overrides before CRM upsert | S3 XLSX + PostgreSQL → HubSpot | [Contact overrides](spreadsheet-contact-overrides.md) |
| Project document inventory into relational reporting | MongoDB → MySQL | [Inventory projection](document-inventory-projection.md) |
| Archive a file without changing its bytes | HTTP → S3 | [File archive](opaque-file-archive.md) |

The [public examples repository](https://github.com/Braidplane/examples) owns the complete specifications, operations, binding templates, fixtures, expected results and qualification records. Its [manifest](https://github.com/Braidplane/examples/blob/main/manifest.json) is the entry point for automation. These pages explain which recipe to choose; they do not maintain another copy of its spec.

## Before you run

Choose provider resources appropriate for your evaluation. Start with the repository's [shared setup and execution guide](https://github.com/Braidplane/examples/blob/main/GUIDE.md). It covers authentication, Git authoring, connections, saved operations, logical bindings, target validation, exact-version deployment, run inspection and cleanup. Provider permissions and destination effects are separate from Braidplane permissions.

Build the CLI from the same Core revision as your application. See [CLI installation](../getting-started/cli.md) and [pipeline specifications](../getting-started/pipeline-specifications.md) for setup.

The recorded examples used public CLI v0.1.0-alpha.6 because alpha.5 lacked the required left-join support. That historical offline validation does not qualify alpha.6 against your current server.

## What the evidence establishes

The initial five specs passed source-built CLI validation and controlled local execution with independent destination comparisons. The sixth, advanced Supplier Price Desk demo retains its separate historical local rehearsal and independent reporting-table reads; its spec passed fresh public alpha.6 offline validation. PostgreSQL, MongoDB, MySQL and S3-compatible storage used local containers. HTTP used a synthetic fixture; BigQuery and HubSpot used local protocol mocks. These records do not establish acceptance in Google Cloud or a HubSpot account, provider retries, or production readiness.

Each recipe keeps its exact executed version/run identity and complete step evidence. A new run requires its own evidence. Read the [coverage and friction ledger](https://github.com/Braidplane/examples/blob/main/COVERAGE.md), including the observed transform-contract failure and missing-binding rejection, before adapting a recipe.

The earlier [PostgreSQL-to-S3 walkthrough](../getting-started/first-pipeline.md) remains a separate guided introduction. Its downloadable assets describe that tutorial; they are not alternative copies of this collection.
