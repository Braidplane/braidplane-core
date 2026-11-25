# Read revenue data from Stripe

## Integration status

**Preview.** [Support levels and provider evidence](support-levels.md#evidence-record)

- **Available.** Read customers, subscriptions, invoices and PaymentIntents using Stripe Apps OAuth or a restricted key.
- **Limits.** Each read returns one page. Continue with the returned cursor and re-read relevant pages for later changes. Live account acceptance is pending.
- **Not integrated.** Writes, automatic incremental checkpoints and deletion tracking.

Use Stripe as a pipeline source to join customers, subscriptions, invoices and payments to CRM or warehouse rows. Payments are Stripe PaymentIntents. They can include unsuccessful or incomplete attempts. Use the status and amount received when reconciling revenue. This connector only reads records.

## Connect an account

Choose **Stripe** in your organization's data sources and select test or live mode. Use separate connections for each account and mode.

**Connect with Stripe** opens Stripe's consent screen when the deployment has an OAuth app configured for the selected mode. Select the account, review the app's read permissions, and approve the installation. Braidplane stores the grant for that organization's source and refreshes it for subsequent reads. Disconnecting in Braidplane stops this source from using the grant. To remove the app’s access to the Stripe account, uninstall it in the Stripe Dashboard. To restore access, reconnect the account. For CLI setup, run `braidplane data-source provider onboard stripe --name Revenue --stripe-mode test` and open the returned authorization URL.

**Restricted API key** works independently of the deployment's OAuth app. Enter a restricted API key issued by the Stripe account you want to read. Give it read access to Customers, Subscriptions, Invoices and PaymentIntents. Test mode requires an `rk_test_` key and live mode requires an `rk_live_` key. The connector connects directly to the key's account. It accepts no connected-account override.

Test the connection, save it, then refresh the schema. The connection test lists one customer. Each subsequent read also requires permission for its selected resource. Schema refresh describes the fixed fields below. To recover from an expired or revoked key, replace the stored credential with a restricted key from the same account and mode and test again.

For CLI setup with a restricted key, put the key in a private file beside this connection JSON. The CLI resolves `api_key_file` locally. The API accepts the resolved value in `credentials.api_key`.

```json
{
  "type": "stripe",
  "name": "Stripe test revenue",
  "slug": "stripe-revenue",
  "config": {"mode": "test"},
  "credentials": {"api_key_file": "restricted-key.txt"}
}
```

Run `braidplane data-source test --file connection.json`, then `braidplane data-source create --file connection.json` in the intended organization.

## Read a page

Save this statement as a query, or use it inline in a pipeline query step with `operation_type: stripe`.

```json
{
  "type": "stripe",
  "operation": "list",
  "object_type": "invoices",
  "limit": 50
}
```

Each row contains `id`, `created` (Unix seconds) and `livemode`. Monetary amounts are integers in the currency's smallest unit.

| Object type | Additional columns |
|---|---|
| `customers` | `name`, `email`, `currency` |
| `subscriptions` | `customer`, `status`, `currency`, `cancel_at_period_end` |
| `invoices` | `customer`, `status`, `currency`, `amount_due`, `amount_paid`, `amount_remaining` |
| `payments` | `customer`, `status`, `currency`, `amount`, `amount_received` |

Reads return one page in Stripe's list order, newest first. Set `limit` from 1 to 100 (default 100). When `has_more` is true, pass `next_cursor` as `starting_after` on the next read of the same object type. For a reusable saved query, set `"starting_after": "{{cursor}}"` and declare a string parameter named `cursor`. Pass an empty string for the first page.

A cursor continues an older page. It does not track subsequent updates, newly created objects or deletions. Pipeline runs have no automatic incremental checkpoint. Re-read the relevant pages to reconcile later status changes. Canceled subscriptions are included. Rate-limited reads fail so callers can back off before retrying the same page. Canceling the run cancels the in-flight request.
