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

This Linux cron job runs the full backup window at 02:15 each day when the scheduler uses UTC.
Set up monitoring that can reach the on-call operator even if the backup host goes offline.
Alert if the job fails, runs too long or misses a scheduled start. Independently monitor the
age of the last verified backup using the time its writers stopped. Before enabling the job,
send a test alert and record who acknowledged it.

```cron
15 2 * * * /usr/local/sbin/braidplane-backup-window
```

Run the script under a dedicated account with a protected local backup directory.
On a separate recovery host, use encrypted storage and give this installation its own
backup directory with mode 0700. Restrict SSH access, pin the host key and install the
matching backup tools. Supply and test these three commands on the installation host
before enabling the schedule.

- `freeze-and-drain` refuses to start if a previous window is still open, even after a reboot.
  Before restricting ingress, it saves the attempt ID, UTC start time, current settings
  and last accepted schedule occurrences in a protected record that survives a restart.
  It limits ingress to the operator and disables triggers, producers and notifications.
  It waits for queued jobs, running jobs and destination writes to finish, with a deadline.
  It also checks for external writers and available storage. If the deadline passes or the
  outcome of a write is unclear, it stops and alerts the operator.
- `check-resume` confirms that the installation, project, volume, keys and release match
  their original values. It checks that work has drained, ingress is still restricted,
  and neither a restore hold nor an upgrade is active. Before starting workers, decide
  which missed schedule occurrences to run and which to skip.
- `reopen-and-observe` checks that the roles are ready and verifies HTTPS through the
  operator route using the trusted CA. It then reopens ingress and restores only the
  producers, triggers and notifications that were enabled before the freeze. Record when
  ingress becomes usable and the elapsed time since the freeze. Keep the window open
  until you verify the next scheduled run's output at the destination. If verification
  fails or times out, preserve the record for the operator to resolve before the next window.

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

On the recovery host, protect finalized backups and their receipts with an independent
retention policy. In the protected operating record, save the installation and attempt IDs,
receipt digest, writer-stop time and verification time. To find the last recovery point,
check finalized backups for this installation, newest writer-stop time first. Use the first
one whose receipt verifies on the recovery host. Skip every `.partial` directory, even if
its local receipt was valid. If none verifies, report that there is no verified recovery point.

### If the scheduled job fails

The alert should name the installation, attempt and failed step. Include the state of
ingress and application roles, and the last verified recovery point if one exists.
Check `lifecycle status` first. A preflight failure can leave all roles running.
If the stop failed partway through, some roles may still be running.

Keep partial files for diagnosis. To retry a transfer, keep the window open, verify
the local backup again and copy it to a fresh staging directory. If time runs out,
check whether the site's recovery contract allows the age of the previous verified
point. The operator must explicitly accept that exposure before running the usual
resume checks and reopening the original installation. Record the missed backup.
Keep the alert open until a new off-host backup verifies.

Keep ingress restricted and escalate if you cannot confirm which installation you
have, whether work drained, which writes reached their destination or how much data
you could lose. Never use an exit trap that unconditionally starts services or reopens ingress.

### Verify the next scheduled run

Deploy the bundled [first-result pipeline](INSTALL.md#first-result-from-customer-git)
on a synthetic installation. Enable a schedule and record its next occurrence, trigger,
timezone, spec hash and destination branch before the freeze.

After reopening ingress, find that occurrence and its run with
`bp pipeline trigger fire list TRIGGER`. Follow the installation guide to watch the run
and retrieve its details. Check that the scheduled run succeeded with the recorded spec hash.
Use the run's output path to find the file on the destination branch. Fetch it at the
commit that wrote it. Compare the decoded JSON with `[{"result":42}]`. Record the
occurrence, run, commit, comparison result and UTC completion time separately from ingress downtime.

Repeat the rehearsal with a deliberately failed copy to a disposable destination.
Check the operator inbox, acknowledge the alert and confirm that the previous verified
backup is still the last recovery point.

### Tagged rehearsal on 2026-11-24

The Linux ARM64 bundle for `v0.3.0-rc.4` (Core revision
`17aa1bfd69a0b8ae41c16b383e9e2a568b1553fa`) ran on a new loopback-only
installation with a synthetic owner. The archive SHA-256 matched the release
record (`73a1c86e2255a0ce4fda1e8563fbfa8cb5760c398704fd635e5da16b968b6753`)
and all bundle checksums passed. The operator started both windows manually.
Each time below runs from the backup command to the first verified HTTPS response.

| UTC window | Result |
|---|---|
| 22:07:32–22:08:13 (41 s) | Standalone backup stopped all five application services. The database stayed healthy. The receiver copy passed receipt verification and an isolated restore probe (schema 8, 8.723 s). After lifecycle start, API, frontend, worker, executor and database reported healthy and the edge returned HTTP 200 for the login page. |
| 22:08:41–22:08:57 (16 s) | The second local backup verified. Omitting the dump from its receiver copy made verification exit 1. The copy stayed partial. The first copy reverified with writer-stop time 22:07:39.268666 UTC. The synthetic operator inbox received the failed attempt and prior recovery point. With an idle fixture, original database healthy and all application roles stopped, the operator resumed the same installation. HTTPS login and subsequent synthetic session creation returned HTTP 200. |

The receiver was a protected directory on the same host and the inbox was a
local Mailpit fixture. The rehearsal still needs an off-host copy, an alert from the site scheduler,
a drain with active work, and a check of the next scheduled run's destination commit.

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
