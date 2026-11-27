# Database change capture experiment

[Core issue #248](https://github.com/Braidplane/braidplane-core/issues/248) evaluates a
source contract for changes that must survive a reader interruption. The existing
bounded query operations depend on watermarks or filters. Those operations do not
provide a retained record of deleted rows.

## Decision

Start with PostgreSQL logical decoding via `pgoutput` in a separate adapter issue.
Limit the first adapter to one PostgreSQL primary, database, operator-managed
publication, persistent logical slot, and ordinary table with a stable primary key.
Capture committed INSERT, UPDATE, and DELETE for the selected table. Use a slot-linked
initial snapshot and resume from a durable transaction end LSN. The PostgreSQL
fixture paired its snapshot and log without taking a global write lock.

Require an operator to approve the replication account and the database it can
access. PostgreSQL has no publication ACL, so publication membership alone cannot
isolate the stream from a replication-capable user. Require an explicit fresh
snapshot after history loss or a source identity change. Cap WAL retention and
monitor slot state and lag. Keep failover, two-phase commit, schema changes, RLS
tables, TRUNCATE, and broad publication discovery outside the first adapter.
[PostgreSQL replication security](https://www.postgresql.org/docs/18/logical-replication-security.html) ·
[Replication slots](https://www.postgresql.org/docs/18/view-pg-replication-slots.html)

Defer MySQL because the tested snapshot handoff needs a global read lock and
instance-wide binlog grants. A future adapter also needs to prove rotation
continuity and row-payload decoding. Defer MongoDB until a reader can obtain its own
snapshot timestamp, finish a multi-batch scan within the snapshot and oplog windows,
and handle collection incarnation changes. Its collection-scoped grants are the
narrowest of the three. The fixture used a known majority-write time to test the
handoff.

## dlt comparison

dlt's verified PostgreSQL source also uses `pgoutput`, a publication, a logical
slot, and the slot's exported snapshot for its direct initial load. It reads
changes in scheduled runs, keeps batches on transaction boundaries, and confirms
the saved LSN to PostgreSQL when the next run starts. This supports the same
checkpoint ordering proposed here. dlt documents that it can log and continue
when a slot is ahead of the saved position. The proposed Core adapter should
stop in that case because the missing changes may be unrecoverable.
[dlt PostgreSQL replication](https://dlthub.com/docs/dlt-ecosystem/verified-sources/pg_replication) ·
[dlt streaming implementation](https://github.com/dlt-hub/verified-sources/blob/master/sources/pg_replication/helpers.py)

dlt's documented MySQL source uses cursor-filtered SQL queries. Its verified
MongoDB source reads collections with `find()` and supports field-based
incremental filters. Those sources give no binlog position or change-stream
resume token for this experiment. This comparison comes from dlt's documentation
and source code. The fixture runner exercises the database interfaces directly.
[dlt SQL source configuration](https://dlthub.com/docs/dlt-ecosystem/verified-sources/sql_database/configuration) ·
[dlt MongoDB source](https://github.com/dlt-hub/verified-sources/blob/master/sources/mongodb/helpers.py)

## Reproduce locally

From the repository root, with Docker connected to a local daemon and Python 3:

```sh
python3 doc/cdc-experiment/run.py all
```

Pass `postgres`, `mysql`, or `mongo` to run a single probe. The runner pins the
images by digest, starts uniquely named containers with no Docker network or
published ports, and removes those containers and their anonymous volumes on exit.
It uses only synthetic data and fixture identities. Keep Python assertions enabled.
A successful run prints a `PASS` line for every selected engine. Positions, tokens,
and source identities vary per run.

The seed row or document has key `0`. Each writer inserts key `1` beyond the
snapshot boundary, with the snapshot read still pending. The reader sees and leaves
that insert unacknowledged and ends the read. The writer then updates and deletes
key `1`. A fresh reader first uses the old checkpoint, then opens again from the
insert's committed position or event token. Each probe then makes the original
history unavailable and verifies that the old checkpoint fails.

## Observations

| Engine | Position and observed order | Snapshot handoff | Retention test |
| --- | --- | --- | --- |
| PostgreSQL 18.6 | `pgoutput` sent BEGIN, change, COMMIT for each write in commit order. COMMIT carries a commit LSN and a separate end LSN. Acknowledging the insert's end LSN left only update and delete on the next peek. | `CREATE_REPLICATION_SLOT … (SNAPSHOT 'use')` bound a repeatable-read scan to the slot's consistent point. The snapshot contained key `0` and the slot delivered the concurrent insert. | A zero `max_slot_wal_keep_size`, WAL segment switches, and checkpoints left the slot `lost` with `wal_removed`. The next peek failed. |
| MySQL 9.4.0 | One binlog file carried `Write_rows`, `Update_rows`, and `Delete_rows`, each followed by an `Xid` commit. Reopening at the insert commit's `End_log_pos` returned the update and delete transactions. | An admin acquired `FLUSH TABLES WITH READ LOCK`. The reader established an InnoDB repeatable-read snapshot and recorded the binlog position before the admin unlocked. The snapshot contained key `0` after the concurrent insert committed. | The admin rotated the log and purged the old file. Reading the original position failed with a missing-log error. |
| MongoDB 8.2.12 | A collection change stream returned insert, update, and delete for key `1`. Reopening with `resumeAfter` on the insert token returned the same update and delete tokens as replay from the old checkpoint. The update carried a field delta and the delete carried a document key. | A snapshot read at majority-write time `T` returned key `0`. The reader observed the seed insert again because `startAtOperationTime: T` is inclusive. It then received the concurrent insert. A fixed-time snapshot already includes the boundary writes. | Unrelated writes advanced the oldest entry of a 16 MiB oplog past the saved token. `resumeAfter` failed with code 286 (`ChangeStreamHistoryLost`). |

One complete run on Core base `01fb1342` recorded these positions:

| Engine | Snapshot boundary | Insert | Update | Delete |
| --- | --- | --- | --- | --- |
| PostgreSQL | `0/1BDFC00` | `0/1BDFCB8` | `0/1BDFD38` | `0/1BDFDA8` |
| MySQL, `cdc-bin.000003` | `1688` | `1979` | `2293` | `2583` |
| MongoDB, event timestamps for reading the trace | `Timestamp(1795791102, 6)` | `Timestamp(1795791102, 7)` | `Timestamp(1795791102, 8)` | `Timestamp(1795791102, 9)` |

The PostgreSQL and MySQL rows report transaction ends for the three writes.
The MongoDB timestamps identify the events in the trace. The runner logs and
compares each full resume token.

PostgreSQL's probe used a replication-protocol connection to create the slot and
the SQL decoding interface to peek and advance it. MySQL's probe used
`SHOW BINLOG EVENTS` to read actual binlog event headers and transaction ends.
It did not read raw row images or open a live dump stream. MongoDB used a
single-node replica set and new change-stream cursors on reconnect.
[PostgreSQL protocol](https://www.postgresql.org/docs/18/protocol-replication.html) ·
[PostgreSQL message layouts](https://www.postgresql.org/docs/18/protocol-logicalrep-message-formats.html) ·
[MySQL binlog event inspection](https://dev.mysql.com/doc/refman/8.4/en/show-binlog-events.html) ·
[MongoDB change streams](https://www.mongodb.com/docs/manual/changestreams/) ·
[MongoDB snapshot reads](https://www.mongodb.com/docs/manual/reference/read-concern-snapshot/)

## Grants used

Each capture identity completed the snapshot and log read, and its attempt to
delete source data was denied.

| Engine | Capture identity | Operator actions |
| --- | --- | --- |
| PostgreSQL | `LOGIN REPLICATION`, database `CONNECT`, schema `USAGE`, and table `SELECT`. The replication connection must pass the server's database authentication rules. | Own the table/publication setup and enable logical WAL. Cap and monitor slot retention, and arrange explicit slot removal when retiring the reader. |
| MySQL | Global `REPLICATION SLAVE` for binary log reads, global `REPLICATION CLIENT` for `SHOW BINARY LOG STATUS`, and `SELECT` on the snapshot table. A table-level grant does not scope the replication log. | Configure row logging. The separate account holding the tested global read lock needs `FLUSH_TABLES` or `RELOAD`. Rotation and purge in the probe ran as fixture admin. |
| MongoDB | Custom role with `find` and `changeStream` on just `sample.items`. | Configure a replica set and create the role. Oplog reads and writes used for the expiry test ran as fixture admin. |

[PostgreSQL replication grants](https://www.postgresql.org/docs/18/logical-replication-security.html) ·
[MySQL snapshot coordinates](https://dev.mysql.com/doc/refman/8.4/en/replication-howto-masterstatus.html) ·
[MySQL position grants](https://dev.mysql.com/doc/refman/8.4/en/show-binary-log-status.html) ·
[MySQL lock grants](https://dev.mysql.com/doc/refman/8.4/en/flush.html) ·
[MongoDB change-stream access](https://www.mongodb.com/docs/manual/changestreams/#access-control)

## When to advance a checkpoint

Save the snapshot boundary with the completed snapshot. After the handoff, advance
only when the reader can recover all accepted changes through the new position
after a crash, including deletes. If durable capture and its checkpoint are
separate writes, commit the capture first. A failure between the two writes must
allow replay from the older checkpoint. Treat loss of history as a stop that
requires an explicit recovery decision. Starting at the latest position after a
lost checkpoint would skip the missing interval.

- **PostgreSQL.** Persist the transaction's end LSN only after every accepted
  change in that committed transaction is durable. Send slot flush feedback after
  persisting that checkpoint. Fence the checkpoint to the source, database,
  publication, and slot, and reject a slot confirmed ahead of the reader's durable
  position. Server crash recovery can rewind a slot's reported position and
  produce duplicates. [PostgreSQL logical decoding](https://www.postgresql.org/docs/18/logicaldecoding-explanation.html) ·
  [Replication start and feedback](https://www.postgresql.org/docs/18/protocol-replication.html)
- **MySQL.** Persist the file and commit event's end position together as the next
  start coordinate. Fence to stream identity and prove log continuity across
  rotation. A server UUID by itself cannot identify a reset log generation. Size
  binlog retention for snapshot time and outages. [MySQL log coordinates](https://dev.mysql.com/doc/refman/8.4/en/replication-howto-masterstatus.html) ·
  [Binlog purge](https://dev.mysql.com/doc/refman/8.4/en/purge-binary-logs.html)
- **MongoDB.** Persist the full BSON token for the last durably accepted event,
  with the source and original watch scope/options. Use `resumeAfter` with the
  same pipeline and options. A post-batch token is safe only once preceding events
  through that position are durable. An invalidation such as a collection drop
  ends the stream. Treat opening a replacement collection as an explicit new
  handoff. [MongoDB resume behavior](https://www.mongodb.com/docs/manual/changestreams/#resume-a-change-stream)

These probes cover a reader restart with three one-change commits and a forced
history-loss case on each engine. Remaining adapter qualification includes
live PostgreSQL replication feedback, multi-change transactions, interrupted
or long snapshots, and source restarts. Implementing any connector and defining
sink delivery guarantees require separate decisions.
