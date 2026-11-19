# Stripe data source

The [Stripe connector guide](../../docs-public/connectors/stripe.md) defines the account and mode boundaries, connection probe, list statement, row schema, pagination and reconciliation limits. Stripe has a source role because revenue joins require only reads. The connector exposes no sink, provider mutations or event triggers.

Create and test requests use `type: stripe`, `config.mode` and write-only `credentials.api_key`. The shared credential service resolves the saved organization's key at execution. The adapter uses the official Stripe Go SDK with a fixed HTTPS endpoint. Schema refresh imports the supported projection into the shared object API schema cache. A refreshed schema does not establish resource permissions.

## Stripe Apps OAuth

A deployment operator registers a Stripe App with OAuth access, public distribution, and the exact callback URI for each enabled mode. Request only `customer_read`, `subscription_read`, `invoice_read` and `payment_intent_read` in its manifest. Stripe supplies environment-specific installation links. Use the external-test links for approved testers. Public install links work after Stripe approves and publishes the app. [Stripe Apps OAuth](https://docs.stripe.com/stripe-apps/api-authentication/oauth) and [app permissions](https://docs.stripe.com/stripe-apps/reference/permissions).

The app developer's API key is a deployment secret for the token exchange. The data source reads with the installing account's access token. The shared credential service stores the grant and rotates refresh tokens. Deployments can run the restricted-key connector without registering a Stripe App.
