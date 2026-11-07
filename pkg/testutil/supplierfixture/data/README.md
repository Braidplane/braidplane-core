# Supplier Price Desk regression fixture

These are the ten synthetic supplier CSV layouts, Cedar update, normalization
statements and independent expected rows used by the Supplier Price Desk rehearsal on 19 October 2026.
Source hashes retain the copied inputs' identity. No customer data is included.

The pipeline keeps the recorded source and normalization operations, replaces
ten writes with one explicit UNION ALL combine and one PostgreSQL upsert, and
retains the logical destination `pricing-database`. Per-provider product
uniqueness remains an input obligation. The combined output allows a product
to occur for multiple providers; its destination key remains organization,
provider and product code.

Baseline and update each contain 30 offers. Cedar's bottle changes from 1.4500
to 2.2000; Packworks at 1.5000 becomes the cheapest bottle. All other suppliers
must remain unchanged. Expected decimals are strings to preserve exact values.

The inline contracts make this regression fixture self-contained. Pinned ODCS
producer/named-input/output/sink provenance is separately exercised by the
compiled-plan runner tests. Hosted acceptance must import the shared canonical
offer contract and use its actual exact revision, with combined and per-input
obligations scoped correctly. Neither these fixtures nor mock runner tests prove
a scheduled staging run or authorize resuming film recording.
