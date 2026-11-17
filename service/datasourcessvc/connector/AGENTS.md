# Connector guide

Follow the [package boundaries](../../../doc/PACKAGE_BOUNDARIES.md#connector-boundary),
[contribution guide](../../../CONTRIBUTING.md) and
[quality gates](../../../doc/QUALITY_GATES.md). Update the
[connector inventory](../../../doc/CONNECTOR_PACKAGE_INVENTORY.md) when a
provider or contract changes.

Keep provider I/O and operation validation in the adapter. Accept typed
config and resolved runtime credentials at construction. Use neutral
payloads, result envelopes and typed errors. Preserve read-only enforcement,
transport policy, cancellation, limits and the sink write guard. Keep credential
lifecycle, persistence, request authorization and pipeline orchestration in
their Core layers. Preserve the typed metadata and assessment contracts for
Proof. Use package-owned, synthetic fixtures.

## Run the HTTP API-key example

From a Core checkout root, use temporary user configuration and run the
[standalone HTTP test](http/auth_test.go):

```sh
(
  connector_config="$(mktemp -d)"
  trap 'rmdir "$connector_config"' EXIT
  XDG_CONFIG_HOME="$connector_config" GOWORK=off bash scripts/go-toolchain.sh test -count=1 -v ./service/datasourcessvc/connector/http -run '^TestAPIKeyHealthExecutionAndSameOriginSchema$'
)
```

The test starts a loopback provider fixture, passes a synthetic runtime API
key directly to the constructor, checks connectivity and executes an
authenticated GET. It follows a same-origin redirect for OpenAPI schema
introspection and checks that the fixture received the key on each request.
Cleanup closes the connector and then the fixture. The test runs without a
Braidplane backend.
