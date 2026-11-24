# Backup and recovery operating record

This recovery procedure still needs qualification. Keep customer work stopped
until reconciliation is complete, even if the database probe passes. Use the
matching installation guide and keep this record with protected recovery evidence.
Never include secret values, raw tokens, row payloads or private keys in logs or
support exports.

## Set the recovery contract before installation

The customer operator records and approves these values; no tighter RPO or RTO
is advertised by this tooling:

| Field | Required operating value |
|---|---|
| Primary and alternate operator | Named people and independent recovery access |
| Backup frequency and maximum recovery-point loss | Interval, maximum tolerated age and timezone |
| Recovery time objective | Duration through reconciliation and verified service resumption |
| Retention | Number/age of daily and pre-upgrade backups, legal constraints |
| Off-host recovery copy | Protected destination, access and independent verification |
| Alert destination and owner | Failed backup, stale last success, failed copy and low disk alerts |
| Capacity | Measured dump, configuration, image and restore working-space requirements |
| Key recovery | Independent protected source, retained IDs and configuration versions |
| Artifact recovery | Original bundle, image digests, signatures/trust roots and Git revision |

Schedule the entire maintenance window described below. The standalone lifecycle
`backup` command stops application roles and leaves them stopped.
The command does not schedule backups, copy them off-host or emit operational
alerts. Arrange and test those operations before promising the recovery contract
above; pre-upgrade backups alone cannot meet a periodic backup schedule.
Retain Git repositories, reachable pinned commits and recovery refs independently;
a database dump cannot recover repository content or unavailable image layers.
Do not prune keys or artifacts while any retained backup still needs them.
Logical backup RPO is the age of the last verified recoverable backup plus any
unreconciled loss. PITR needs a separately qualified base backup and continuous
WAL/archive restore evidence before advertising a tighter objective.

## Freeze, back up and verify

1. Record the incident/upgrade start time and exact release and catalog identity.
   Restrict ingress and disable external producers, schedules and notifications.
   Wait for active work to finish; record unresolved runs and destination effects.
   A forced stop can leave ambiguous external effects and is not a successful drain.
2. Serialize lifecycle operations using the installation lock. Exclude manual SQL,
   other hosts and external database writers; the local lock cannot fence them.
3. Measure available space on both the backup filesystem and Docker storage host.
   Compare with measured working-space requirements above, including candidate
   images, dump and a separate restored database. Stop if capacity is insufficient.
4. Run the documented standalone `backup` command, or the upgrade command for an
   approved transition. Both stop bundled application roles before dumping and
   write a versioned receipt. Upgrade writes it before migration. Preserve the
   receipt, dump, configuration and any upgrade failure journal.
5. Verify the receipt locally and after the protected off-host copy using
   `python3 -m scripts.installation.backup BACKUP_DIRECTORY`. Record the copy's
   identity and verification time. Alert on any failure or excessive backup age.
   Never count an unverified copy or a partial backup as a recovery point.
6. Run `python3 -m scripts.installation.restore_probe BACKUP_DIRECTORY` against a
   trusted backup. It uses fresh isolated storage and starts no application roles.
   Record the result; the disposable database is removed after this probe.

## Example nightly maintenance window

For a Linux installation, one example is a 02:15 job in the scheduler's UTC
timezone. Have the backup operator install the **whole window** as a supervised
job. Configure the scheduler to alert the on-call operator on nonzero exit,
overrun or missed execution, even if the host goes offline. Monitor the age of
the last verified writer-stop time independently. Test delivery and record the
operator's acknowledgement before enabling the recurring job.

```cron
15 2 * * * /usr/local/sbin/braidplane-backup-window
```

The following Bash body assumes a dedicated account, a protected local backup
parent, and a separate encrypted recovery host with restricted SSH access,
pinned host key, matching tools and a mode-0700 backup parent. Use a unique
receiver namespace for this installation. The three absolute-path site commands
are operator-supplied integrations. Install and exercise them before scheduling:

- `freeze-and-drain` first refuses any unresolved window, including after a
  reboot. Before changing admissions it durably records the attempt ID, UTC start,
  original enablement settings and last accepted occurrences in a protected
  window record. It restricts ingress to the operator, disables triggers,
  producers and notifications, and waits with a deadline for queued and running
  work and in-flight destination writes to finish. It checks for external writers
  and storage capacity. On timeout or ambiguous effects it fails and alerts.
- `check-resume` confirms this is the original installation, project, volume,
  keys and installed release, with a clean drain, no restore hold or upgrade in
  progress, and admissions still fenced. Agree the disposition of missed
  occurrences before starting any workers.
- `reopen-and-observe` verifies role readiness and makes a CA-validated HTTPS
  request through the operator route before reopening ingress. Restore only the
  previously enabled producers, triggers and notifications. Record the time
  ingress is usable and total downtime from the freeze. Watch the next scheduled
  occurrence through destination readback as described below. Mark the window
  resolved only after verification. On failure or timeout retain the record for
  operator reconciliation before another window can begin.

```bash
#!/bin/bash
set -eu
umask 077
cd /opt/braidplane/tools
installation=/protected/braidplane
backups=/protected/backups
receiver=braidplane-vault
exec 9>"$backups/window.lock"
flock -n 9
id="$(date -u +%Y%m%dT%H%M%SZ)-$(cat /proc/sys/kernel/random/uuid)"
copy="/protected/backups/$id"
# Coordinate upgrades and manual operations for the entire window as well.
/usr/local/sbin/freeze-and-drain "$installation" "$id"
python3 -m scripts.installation.lifecycle backup "$installation" --backup-directory "$backups/$id"
python3 -m scripts.installation.backup "$backups/$id"
ssh -o BatchMode=yes "$receiver" "mkdir -m 700 '$copy.partial'"
scp -B -p "$backups/$id/"{database.dump,configuration.tar,backup.json} "$receiver:$copy.partial/"
ssh -o BatchMode=yes "$receiver" "set -e
  cd /opt/braidplane/tools
  python3 -m scripts.installation.backup '$copy.partial'
  python3 -m scripts.installation.restore_probe '$copy.partial'
  mv -T '$copy.partial' '$copy'"
/usr/local/sbin/check-resume "$installation"
python3 -m scripts.installation.lifecycle start "$installation"
python3 -m scripts.installation.lifecycle status "$installation"
/usr/local/sbin/reopen-and-observe "$installation"
```

On the vault, keep finalized directories and their receipts under independent
retention protection. Record the installation identity, ID, receipt digest,
writer-stop time and verification time in the protected operating record.
The newest **finalized and verified** writer-stop time for this installation
identifies the last recovery point. Reverify its receipt on the receiver.
Exclude every `.partial` directory even if it contains a valid local receipt.
If no finalized backup verifies, report that no verified point is available.

**On failure**, the scheduler's alert should identify the installation, attempt,
failed step, ingress/role state and last verified point (or none). Inspect
`lifecycle status` first. A preflight refusal may leave roles running and a
failed stop may leave a mixed state. Preserve partial files for diagnosis.
Retry a failed transfer to a fresh staging directory from a locally reverified
backup while the window is held. If the transfer cannot finish within the
window, the operator may explicitly accept the age of the previous verified
point under the site's recovery contract, run the same resume checks, then
start and reopen the original installation. Record the missed backup and keep
the alert open until a fresh off-host point verifies. If the drain, identity,
external effects or recovery exposure are uncertain, keep ingress fenced and
escalate. Never put an unconditional start or reopen in an exit trap.

**Scheduled pipeline proof.** On a synthetic installation, deploy the bundled
[first-result pipeline](INSTALL.md#first-result-from-customer-git) and configure
an enabled schedule with a known next occurrence. Capture the trigger, timezone,
spec hash and destination branch before freezing. After resumption use
`bp pipeline trigger fire list TRIGGER` to find that next occurrence and its run.
Watch and get that run using the installation guide. Require a successful
scheduled run of the recorded version and fetch its exact output commit/path
independently from the destination. Compare the decoded JSON to
`[{"result":42}]`. Record the occurrence, run, commit, comparison and UTC
completion time separately from ingress downtime. Repeat with a deliberately
failed copy to a disposable destination. Read back the alert in the operator
inbox, acknowledge it, and verify the preceding recovery point remains current.

### Tagged rehearsal on 2026-11-24

The Linux ARM64 bundle for `v0.3.0-rc.4` (Core revision
`17aa1bfd69a0b8ae41c16b383e9e2a568b1553fa`) ran on a new loopback-only
installation with a synthetic owner. The archive SHA-256 matched the release
record (`73a1c86e2255a0ce4fda1e8563fbfa8cb5760c398704fd635e5da16b968b6753`)
and all bundle checksums passed. These were manually invoked maintenance windows.
Elapsed times run from backup invocation through the first verified HTTPS response.

| UTC window | Result |
|---|---|
| 22:07:32–22:08:13 (41 s) | Standalone backup stopped all five application services. The database stayed healthy. The receiver copy passed receipt verification and an isolated restore probe (schema 8, 8.723 s). After lifecycle start, API, frontend, worker, executor and database reported healthy and the edge returned HTTP 200 for the login page. |
| 22:08:41–22:08:57 (16 s) | The second local backup verified. Omitting the dump from its receiver copy made verification exit 1. The copy stayed partial. The first copy reverified with writer-stop time 22:07:39.268666 UTC. The synthetic operator inbox received the failed attempt and prior recovery point. With an idle fixture, original database healthy and all application roles stopped, the operator resumed the same installation. HTTPS login and subsequent synthetic session creation returned HTTP 200. |

The receiver was a protected directory on the same host and the inbox was a
local Mailpit fixture. Off-host custody, site scheduler alert routing, draining
active work and the next scheduled pipeline's destination commit remain open
qualification items.

## If an upgrade fails

Keep applications stopped and preserve the upgrade journal, backup and logs.
Fix forward with a reviewed release, or restore the matching database and keys
into isolated storage. Do not start old images against a possibly changed schema,
clear a dirty ledger or apply down migrations. The upgrade command does not try
automatic image rollback.

Migration compatibility follows the repository's forward-only migration history
and normal application startup checks. A separate compatibility matrix, catalog
inspection protocol and automatic rollback are deferred.

## Restore and reconcile before resumption

Restore to new empty storage on an isolated recovery host. Keep production
credentials, provider routing, SMTP, webhook admission and worker execution fenced.
The database probe is disposable. For persistent isolated database recovery use
the packaged `scripts.installation.restore` command from the installation guide.
It uses a new project and empty storage, retains recovered keys/configuration,
and writes a startup hold. Its topology contains no application services and
publishes no ports. Do not delete the hold or restore the original topology to
bypass reconciliation. Service-resumption tooling remains to be qualified.

Recover the original application key, all required product key IDs/material,
execution-envelope keys and matching configuration from the independent source.
Compare exact release/schema identities and grant denials. Use a frozen synthetic
oracle to verify credential resolution, normal runs and history before customer
recovery. Never test customer credentials against live providers during isolation.

Create one protected reconciliation row per affected run/effect:

| Required evidence | Record |
|---|---|
| Run and stable effect identity | Original identity, sink/resource and retained input/checkpoint identity |
| Restore point | Backup writer-stop time, incident cut and known loss interval |
| Independent destination state | Accepted/absent/ambiguous with provider evidence and inspection time |
| Local state | Queue ownership/fencing, checkpoint, replay and completion records |
| Decision | Keep quarantined, reviewed forward repair, or eligible retained-input recovery |
| Approval and readback | Operator, reason, final independent destination and history checks |

Treat an effect as ambiguous until destination evidence resolves it. Automatic
replay is prohibited when checkpoints are missing, ownership is active or unfenced,
or the connector cannot check whether repeating the effect is safe. A restored
queue row cannot establish whether a write happened. Keep workers stopped while
comparing effects after the restore point with independent provider records,
including work whose run row was lost completely.

For eligible crash repair, use the public pipeline run recovery operation with
retained pre-dispatch inputs. Preserve the original effect identity. Rehearse
worker loss before completion and after provider acceptance, terminal queue
fencing and repeated recovery. Change the source after the crash and prove the
repair does not reread it. Inspect the destination independently after every
attempt. A successful run status alone does not prove effect reconciliation.

Reconcile schedules against their last accepted occurrence and missed windows;
choose explicitly whether to skip or execute each missed occurrence. Reconcile
notification/email jobs against actual delivery and suppress ambiguous duplicates.
Review sessions, API/personal tokens, webhook replay records and signing secrets:
restoring can resurrect revoked access and forget consumed deliveries. Revoke or
rotate affected access through supported administrative operations before exposing
the restored installation. Keep the recovery hold until you can determine a safe
action for each item. Do not invent SQL repair commands.

Queued authentication email tokens use the original `ENCRYPTION_KEY_HEX`, not the
product keyring. Keep that original key to read pending jobs. If it is lost or
changed, pending reset/verification jobs cannot be promised to survive. Keep old
jobs suppressed; issue fresh password resets or verification messages through the
normal product flow using the current key. Test failed old-job delivery, absence
of raw tokens in logs and successful fresh-reset continuation. Product credential
readability does not prove queued-email continuity.

Only resume after each reconciliation row has an approved disposition, restored
authorization is checked, and isolated normal-run/history checks pass. Re-enable
one admission path at a time and observe independent destination readback.
Record full recovery elapsed time through this point, actual lost records/events,
remaining ambiguity and comparison with the customer RPO/RTO. Database-probe time
alone is not service RTO.

## Final qualification evidence

A second operator follows the packaged guide without author help on the exact
candidate. Retain exact artifact/catalog identities, commands and pass/fail/skip
results for empty-storage restore with recovered keys and frozen oracle; failed
migration; downgrade refusal; accepted sink
write with lost completion; restore-point data loss; retained-input crash repair;
missing checkpoints/unfenced ownership/ambiguous effects; pending-email failure
and reissue; off-host copy recovery; measured RPO/RTO and final access checks.
Missing or indirect evidence leaves that acceptance item incomplete.
