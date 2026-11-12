# Connectors

Choose a connector by starting with the system that owns the records and the
business action you need next: read, reshape, update, or deliver a file. You
do not need to provision any of these systems to explore the documentation.

All connectors below support pipeline source and sink roles. Actual access
depends on the configured connection, provider permissions, and operation.
Each guide lists the available operations and their limits. See
[support levels and evidence](support-levels.md) for connector status,
maintenance ownership and planned connectors.
Inspect the installed CLI build with `braidplane data-source capabilities`.

| Connector | Typical use |
|---|---|
| [PostgreSQL](postgresql.md) | SQL reads, inserts, updates, and upserts |
| [MySQL](mysql.md) | SQL reads and database-native writes |
| [Google Workspace](google-workspace.md) | Selected-file Sheets, Drive, Docs and Slides operations |
| [BigQuery](bigquery.md) | Warehouse query jobs and row appends |
| [Snowflake](snowflake.md) | Parameterized warehouse reads and INSERT sinks |
| [MongoDB](mongodb.md) | Document queries and writes |
| [HTTP](http.md) | Explicit API requests and response payloads |
| [HubSpot](hubspot.md) | CRM search/list and unique-key upserts |
| [FTP](ftp.md) | Explicit FTPS file reads and writes |
| [Git](git.md) | Manually authenticated repository files |
| [GitHub](github.md) | Repository files through a GitHub App installation |
| [GitLab](gitlab.md) | Repository files through a managed OAuth connection |
| [S3](s3.md) | Structured objects and raw file transfers |

Each guide includes connection settings, authentication and permissions,
connection testing, metadata discovery, read/write examples, operation fields,
pipeline behavior, and troubleshooting. The examples use synthetic data and
state which provider resources must already exist.

## Understand the capabilities

**Connection testing** checks access to the configured system. **Schema refresh**
retrieves metadata, such as columns, CRM properties, file fields, or API
operations. The type and completeness of that metadata depend on the connector.
Queries and writes use the permissions required by their specific operation.

An **operation** is the specific query, request, or file action a step performs.
Its payload must match the next step. SQL transforms require row-normalized
inputs; arbitrary JSON transformations and implicit file conversions are not
available.

Some sources support **incremental checkpoints**: SQL databases, BigQuery,
Snowflake, and MongoDB use watermarks; HTTP supports timestamp watermarks and
cursors, and HubSpot supports cursors. The operation
must apply the corresponding filter or cursor. FTP, Git, and S3 use full reads
for now, as does Google Workspace. Checkpoints are not change-data-capture
or automatic synchronization.

Start with [Connect a system](connect-a-system.md) when you are ready to configure
a connection in your workspace.
