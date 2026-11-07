# Pinned ODCS schema

`odcs-v3.2.0.json` is the unmodified Bitol ODCS v3.2.0 JSON Schema from
[release commit f0bdad95346905d500be5ef4b2c2d9b1d95223b7](https://github.com/bitol-io/open-data-contract-standard/blob/f0bdad95346905d500be5ef4b2c2d9b1d95223b7/schema/odcs-json-schema-v3.2.0.json).
SHA-256: `edb41f33ec46e84780e99872ab2bd67f074959d2bf3e9c9fc54e61f8982b0d93`.
The upstream Apache-2.0 license is included in `LICENSE`.

The installed `github.com/santhosh-tekuri/jsonschema/v6` validator compiles the
embedded Draft 2019-09 schema once, with format assertions and no URL loader.
Braidplane independently requires exactly `apiVersion: v3.2.0`, since this
upstream schema also accepts earlier API versions. Upgrading the schema requires
an explicit version decision and rerunning the fixture corpus; no rolling URL
is used during validation.

Admission also applies pinned normative documentation corrections in memory:
Unicode whitespace in stable IDs, array-shaped schema objects, inert `file:///`
references, and unresolved tokens in string positions. The vendored file remains
unmodified. The additional semantic checks and source-linked conformance corpus
are inventoried in ODCS v3.2.0 admission.
