# Pipeline examples

Choose a task, then use its canonical recipe to set up, validate, deploy, run and inspect a pipeline with synthetic data.

| Task | Flow | Recipe |
| --- | --- | --- |
| Enrich API orders while keeping unknown customers | HTTP + PostgreSQL → S3 JSON | [Customer enrichment](http-customer-enrichment.md) |
| Reconcile payments, refunds and unmatched ledger entries | S3 CSV + PostgreSQL → BigQuery | [Settlement reconciliation](settlement-ledger-reconciliation.md) |
| Apply spreadsheet overrides before CRM upsert | S3 XLSX + PostgreSQL → HubSpot | [Contact overrides](spreadsheet-contact-overrides.md) |
| Project document inventory into relational reporting | MongoDB → MySQL | [Inventory projection](document-inventory-projection.md) |
| Archive a file without changing its bytes | HTTP → S3 | [File archive](opaque-file-archive.md) |

The [public examples repository](https://github.com/Braidplane/examples) contains the pipeline definitions, operations, binding templates, sample data and expected results. Use its [manifest](https://github.com/Braidplane/examples/blob/main/manifest.json) to find inputs programmatically.

## Before you run

Start with the repository's [shared setup and execution guide](https://github.com/Braidplane/examples/blob/main/GUIDE.md). It covers authentication, Git authoring, connections, saved operations, bindings, deployment, run inspection and cleanup. Use dedicated test resources and grant the provider permissions listed in each recipe.

Build the CLI from the same Core revision as your application. See [CLI installation](../getting-started/cli.md) and [pipeline specifications](../getting-started/pipeline-specifications.md) for setup.

The original runs used the source revision recorded with each recipe. Separate offline checks used public CLI v0.1.0-alpha.6; alpha.5 lacked the required left-join support. Build a matching CLI for your application before running them.

## Test fixtures and results

The five recipes above were run locally and their destination output was compared with the expected results. PostgreSQL, MongoDB, MySQL and S3-compatible storage used local containers. HTTP used a synthetic fixture, while BigQuery and HubSpot used local protocol mocks. To run against Google Cloud or HubSpot, configure your own account and permissions.

The examples repository also includes an advanced [settlement review](https://github.com/Braidplane/examples/blob/main/recipes/settlement-review/README.md) demo with local execution and reporting-table checks. Its specification passed offline validation with public CLI alpha.6.

Each recipe records its executed version, run and step results. The [coverage and friction ledger](https://github.com/Braidplane/examples/blob/main/COVERAGE.md) lists test scope and known failures, including a transform-contract mismatch and a missing binding.

For a guided introduction with downloadable inputs, follow the [PostgreSQL-to-S3 walkthrough](../getting-started/first-pipeline.md).
