# Small self-hosted footprint

## Core v0.2.0 on a small Linux host

On 24 November 2026, Core v0.2.0 installed, ran its bundled three-step pipeline
and completed a forward image upgrade on **2 vCPUs, 4 GiB RAM and a 20 GiB disk**.
This was the smallest configuration tested. The Lima guest ran Ubuntu 24.04.5
LTS on ARM64 with Docker Engine 29.1.3 and Compose 2.40.3. Its root filesystem
held the OS, image layers, PostgreSQL data, backup and restore scratch space.

| Phase | Elapsed | Whole-host RAM | Used root disk after phase |
| --- | ---: | ---: | ---: |
| Prepared guest before image pulls | — | — | 2.35 GiB |
| Install published bundle and wait for readiness | 39.88 s | 880.9 MiB peak | 3.83 GiB |
| Idle after install | 30.08 s | 854.7 MiB median, 863.0 MiB peak | — |
| Run pipeline and independently read back its Git output | 10.21 s | 849.4 MiB peak | 3.88 GiB |

The host ran PostgreSQL, the API, worker, executor, frontend and Caddy edge.
Setup also used the migrator, grants, bootstrap and CLI containers. The one-shot
CLI image was pulled after the idle window. GitHub hosted the synthetic Git
repository. SMTP was configured but unused. The timed run includes CLI startup,
polling and a shallow clone for verification. The worker read 41 from Git, added
one in SQL and wrote one JSON row. The independent clone contained
`[{"result":42}]` at the reported sink path.

### Disk for the first forward upgrade and backup

The [v0.2.0 ARM64 stable download](https://github.com/Braidplane/braidplane-core/releases/tag/v0.2.0-linux-arm64)
contains the promoted v0.2.0-rc.25 manifest. The next published patch candidate
was [v0.2.1-rc.1](https://github.com/Braidplane/braidplane-core/releases/tag/v0.2.1-rc.1-linux-arm64).
Both pin the same PostgreSQL image and migration catalog version 8. The candidate
bundle completed its forward upgrade, restarted the application roles and
recorded the candidate identity. This rehearsal covers that specific transition. Upgrades that change the
PostgreSQL image or migration catalog need separate measurements.

| Phase on `/dev/vda1` | Additional space at sampled peak | Elapsed | Verification |
| --- | ---: | ---: | --- |
| Pull candidate images, back up the stopped application, upgrade and verify backup | 642.5 MiB | 29.69 s | Verified 688,784-byte database dump and 61,440-byte configuration archive |
| Restore the backup into a separate database with old images and backup still present | 71.9 MiB | 1.91 s | Restored the dump, applied grants and checked for one clean migration ledger entry |

The upgrade measurement starts before the first candidate pull, including the
optional CLI. Docker, containerd and the dump shared one filesystem, so 642.5 MiB
includes their combined working space. Restore uses a fresh post-upgrade baseline.
Database size, image-layer reuse, WAL and concurrent work can change both peaks.
The restore probe exercises database recovery only. The
[whole-host receipt](evidence/235/whole-host.json) records exact free-byte
low points, sample counts, timings, artifact digests and the verified output commit.

## Equivalent workflow on a shared test machine

On 16 November 2026, each tool read the same 12 synthetic orders from PostgreSQL
16 and wrote a full snapshot to its own MinIO bucket. The records included nulls,
Unicode, embedded quotes, newlines and integer money. Each run downloaded the
destination objects and verified field types, values, row count and uniqueness.

| Tool and deployment | Median idle memory (MiB) | Sampled application peak (MiB) | Median elapsed through verification |
| --- | ---: | ---: | ---: |
| Core pre-v0.0.1 (unreleased source build), native supervisor, API, worker and frontend, containerized PostgreSQL 18.6 and Git | 123.2 native / 143.0 containers | 167.4 native / 159.7 containers | 3.012 s |
| Airflow 3.3.2, Python 3.12, Celery worker, API, scheduler, DAG processor, PostgreSQL 16 and Redis | 1,297.5 | 1,395.9 | 4.101 s |
| Airbyte 2.4.0, dedicated kind node, PostgreSQL connector 3.8.9 and S3 connector 1.9.10 | 4,638.2 | 5,559.3 | 27.605 s |
| Kestra 2.0.8 standalone with PostgreSQL 16 and the PostgreSQL, AWS and serialization plugins | 626.8 | 647.1 | 4.068 s |

The [Core source revision](https://github.com/Braidplane/braidplane-core/commit/3315c4eb3e7c95ebdf8e20827a348507d0711cbe) predates the first tagged release.
The host was an Apple M3 Pro with 12 logical CPUs and 36 GiB RAM. Its ARM64
Linux Docker VM exposed about 23.5 GiB. Each deployment ran separately.
Timing includes the client request, status polling, download and verification.
The median covers three warm repeats after one excluded warm-up.

Memory uses complete one-second samples. Idle is the median during the idle
window. Core's native column measures macOS RSS. Container columns measure
Linux working set, including the entire kind node for Airbyte. The shared
source and MinIO consumed another 264–289 MiB at sampled peaks. Measurements
exclude the VM overhead, clients and sampler. Short spikes can fall between
samples. These boundaries preclude a whole-host memory ratio or a VPS sizing
recommendation. Whole-host disk after installation and after the run is
unavailable for all four historical deployments.

To repeat the small workflow, load the [12 input records](evidence/235/orders.json)
into a fresh PostgreSQL orders table. Use integers for IDs and cents, text for
all other fields, and preserve nulls. Configure each tool for one manual full
snapshot to a fresh S3 prefix. Run a warm-up and three repeats. For each repeat,
download only the objects written by that run. Parse the JSON row array (for
Airbyte, take `_airbyte_data` from each JSONL record). Sort by `order_id` and
compare all eight fields of every row with the input file. The
[run receipts](evidence/235/warm-runs.json) give the elapsed samples, memory
boundaries, object hashes and canonical destination hashes.

For the separate million-row transfer, retained-input recovery and destination
checksum, see the [large SQL transfer qualification](../doc/LARGE_SQL_RECOVERY_QUALIFICATION.md).

## Hosting price example

On 24 November 2026, Amazon listed its public IPv4 Linux/Unix Lightsail bundle
with 2 vCPUs, 4 GB memory and an included 80 GB SSD at USD 24 per month.
The provider includes more disk than the 20 GiB local guest. The workload ran
on local hardware.
[Lightsail pricing](https://aws.amazon.com/lightsail/pricing/)

## Collect whole-host measurements for a released bundle

Use a fresh Linux guest. The measured guest used Lima 2.3.0 with the Ubuntu
24.04 ARM64 cloud image, two vCPUs, 4 GiB memory and a 20 GiB virtual disk.
Install Docker Engine and the Compose v2 plugin before taking the baseline. Record its OS, architecture, vCPU count, configured RAM
and disk capacity. Record the release tag, revision, manifest and archive digest,
Engine and Compose versions, image store location and backing filesystem.
Include the metadata database, API, worker, executor, frontend, edge and Docker
daemon. List the locations of Git, SMTP and any workflow fixtures.

1. Follow the selected release's bundled [installation procedure](../scripts/installation/INSTALL.md).
   Before pulling any images, record `lsblk -b`, `free -b` and
   `df -B1` for the OS, Docker image store, containerd store and backup destination.
   Repeat the filesystem readings after install and after verifying the run.
2. Copy [sample_host.py](sample_host.py) into the guest and run it there. This example assumes one root filesystem.
   Add `--path` for each separate store and the existing backup parent directory.
   Use a fresh receipt filename for each phase.

   ```sh
   python3 comparison/sample_host.py --path / --receipt idle.json -- sleep 30
   ```

3. Prepare the bundled synthetic first-result pipeline. Wrap the start, watch
   and independent Git destination readback in one shell script with
   `set -eu`. Verify the new output commit and compare its parsed JSON with
   the bundled expected row. Use a branch reserved for the test, with an issue-dated
   output path. Then run that script in place of `sleep 30`.
   Record elapsed time and the peak for that run separately from idle memory.
4. Start another capture immediately before the candidate's first image pull.
   Run the matching candidate bundle's lifecycle upgrade and validate its backup
   receipt. Record the dump size and the available-space low point for each
   filesystem. Leave the original images and backup in place. Paths on the same
   device share one low point, so count it once.
5. Start a fresh capture before invoking the bundle's restore probe on that backup.
   Report its own peak additional bytes relative to the post-upgrade baseline.
   Record the probe exit status. See the [backup and recovery procedure](../scripts/installation/RECOVERY.md).

The sampler reports guest RAM as `MemTotal - MemAvailable` and free disk as
blocks available to an unprivileged process. It samples about every 100 ms.
The minimum records space remaining. Subtract it from starting free space to
get `peak_additional_bytes`, the additional headroom consumed. Shorter spikes
can escape sampling, so that difference is a lower bound. Preserve the receipts alongside
the exact release identities and the independent destination verdict.
