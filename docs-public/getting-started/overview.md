# How Braidplane works

For an assessment of an exact Git change before applying it, see
[Review a pipeline change with Proof](proof.md).

Imagine an operations team that needs to combine order records with customer
information and send a useful result to another system. Braidplane lets the
team describe that work as a pipeline, select where it runs, and inspect each
execution.

This is a reading tour. You do not need to install anything or connect a system
to follow it.

## 1. Choose the systems involved

A **connector** describes how Braidplane talks to a kind of system, such as a
database, HTTP API, CRM, or file store. A **data source** is one configured
connection to a specific system. Despite the name, a data source can also be
a destination when its connector and permissions allow writes.

For our example, identify where orders and customers live and where the result
should go. These choices determine which connectors and operations to use;
PostgreSQL and S3 are not platform requirements.

[Explore the connector capabilities](../connectors/index.md) before choosing
systems when you are ready to build a workflow.

## 2. Describe the work

A **pipeline** connects explicit steps:

```text
Read orders ─────┐
                 ├─ Join and select fields ─ Send the result
Read customers ──┘
```

A source operation specifies which records or files to read. A transform
specifies how to combine or reshape compatible inputs. A sink operation
specifies the write to perform. For example, an SQL transform can join
row-shaped order and customer inputs by customer ID.

The definition includes the data shapes expected between steps. A connector
being available does not mean every output format can enter every transform;
file bytes, JSON documents, and rows have different capabilities.

Pipeline definitions are versioned through Git. Credentials belong to the
configured connections, outside the pipeline definition.

## 3. Organize the workflow

An **organization** owns the connections and controls who can access them.
A **project** groups related pipelines. A **project environment** binds the
pipeline's logical connection names to saved data sources.

For example, the pipeline can refer to `orders` while a development environment
maps that name to a connection containing synthetic orders. Another environment
can have its own binding. The definition can stay the same while the selected
connections differ.

## 4. Validate and deploy a version

**Validation** checks the pipeline structure, bindings, and available cached
metadata. It does not move data, refresh live schemas, or prove that a remote
system will accept a write.

A **deployment** selects an immutable pipeline version for a project
environment. This makes the version and connection bindings explicit before
execution.

## 5. Run and inspect

A **run** executes the deployed workflow. Inspect its status, step timeline, and
failure diagnostics to understand what completed and where execution stopped.
If a step fails, inspect its diagnostics before deciding whether to retry;
write and replay behavior depends on the connector and operation.

For our example, a successful run means the configured reads, join, and write
completed. Confirming that the destination contains the intended records is a
separate check against that system.

## Where to go next

- Sign in to the deployment you intend to use, then choose a workspace for the workflow.
- [UI tutorial](first-result.md): connect any supported system and run one safe read in the app.
- [Install the CLI](cli.md), then follow the [CLI tutorial](first-result-cli.md) for the same connector-agnostic result.
- [Pipeline specifications](pipeline-specifications.md): learn the file format, steps, references, contracts, and validation commands.
- [Connector guide](../connectors/index.md): choose systems and understand capabilities.
- [Connect a system](../connectors/connect-a-system.md): learn the shared setup flow.
- [API reference](../api/index.md): explore the API contract.

An optional [PostgreSQL-to-S3 walkthrough](first-pipeline.md) illustrates a full
execution with reader-owned infrastructure and an available CLI. It is a
worked example for teams with those prerequisites, rather than a prerequisite
for learning Braidplane.
