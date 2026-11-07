# Local instance administrator provisioning

The local operator executable is built from `cmd/admin` as `out/braidplane-admin`.
The backend container includes it at `/braidplane-admin`; select that executable
with `--entrypoint` when running an operator command in the image.

`braidplane-admin grant --user <slug>` and `braidplane-admin revoke --user <slug>` are local database-operator commands. They do not use HTTP, signup, passwords, personal access tokens, or email addresses.

The target must already be a verified account. Grant and revoke are idempotent. Revocation refuses to remove the final instance administrator. Every changed role is recorded with the target slug, old and new roles, database operator identity, and timestamp in the same transaction.

The role is global and grants no organization membership. Its audit records have
no tenant foreign key.

## First administrator with closed registration

On a new installation, a trusted database operator can create the first local
administrator without opening HTTP signup or configuring Google:

```sh
braidplane-admin bootstrap --user owner --name 'Instance Owner' \
  --email owner@example.test --email-ownership-verified < /protected/bootstrap-password
```

Supply one password (8–1024 bytes) on stdin, optionally followed by a newline.
Use a protected temporary file or secret-manager pipe; do not place the password
in command arguments, shell history or logs. Remove the temporary file after use.
The operator must independently verify ownership of the supplied email address;
the explicit flag records that responsibility but does not perform SMTP
verification. The account can then use normal local login and SMTP
password recovery. No OAuth credentials, vendor service, or public signup are
needed for this command.

Bootstrap creates a new account and grants its role in one transaction, sharing
the lock used by grant/revoke. It refuses to run if any instance administrator
exists and never overwrites an existing account or password. Conflicting emails
or slugs cause the command to fail; it never adopts an existing account.
The role audit records the database operator. The new administrator has no
organization membership. Use the normal verified-account grant/revoke commands
for subsequent role management.

Run with the installation's intended configuration and a protected operator DB
identity. The command is unavailable over HTTP. The operator remains responsible
for database recovery and key custody. Bootstrap cannot restore the original
encryption keys or recover existing users' passwords.
