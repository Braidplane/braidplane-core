# Stripe data source

The [Stripe connector guide](../../docs-public/connectors/stripe.md) defines the account and mode boundaries, connection probe, list statement, row schema, pagination and reconciliation limits. Stripe has a source role because revenue joins require only reads. The connector exposes no sink, provider mutations or event triggers.

Create and test requests use `type: stripe`, `config.mode` and write-only `credentials.api_key`. The shared credential service resolves the saved organization's key at execution. The adapter uses the official Stripe Go SDK with a fixed HTTPS endpoint. Schema refresh imports the supported projection into the shared object API schema cache. A refreshed schema does not establish resource permissions.
