# Core schema reuse for MCP

Core REST and the public CLI use OpenAPI 3.0.4. A catalog mapping can call
`apischema.MCPObjectSchema(componentName)` from `api-schema` for an input or
structured output object. It reads the embedded Core source and emits a standalone
JSON Schema 2020-12 document. The caller selects the operation, public fields,
and direction before exposing a tool. Authorization and credential handling
belong to that operation. The export contains schema metadata only.

## Compatibility decision

The 3.1 trial converted nullable CLI types to a union with null and included
JSON null in nullable enums. oapi-codegen v2.8.2 emitted a Go string constant
`"<nil>"` and accepted it in `Valid()`. The CLI trial also renamed public enum
constants. These change generated client behavior. Recheck the following probe
before migrating the REST source. Its Go enum should contain exactly the two
declared string values.

```sh
bash scripts/go-toolchain.sh tool -modfile=tools/go.mod oapi-codegen \
  -generate types,skip-prune -package probe api-schema/testdata/nullable-enum-3.1.yml
```

- The generator is pinned to v2.8.2. It includes the v2.8.1 strict request
  binding changes and validates generator inputs and their referenced schemas.
  See the [generator release](https://github.com/oapi-codegen/oapi-codegen/releases/tag/v2.8.2).
- The generated runtime is v1.9.0. The Core Fiber v3.5.0 adapter uses
  kin-openapi v0.152.0 for request validation and passes the validated value to
  generated strict handlers through Fiber locals. The generator and runtime
  both support this binding contract. See the [runtime release](https://github.com/oapi-codegen/runtime/releases/tag/v1.9.0).
- The newest parser tested, v0.153.0, changed YAML import behavior. The HTTP
  connector suite detected different numeric-key normalization, rejection of
  binary-tagged description strings, and a missing uppercase generic reference.
  v0.152.0 passes those fixtures and includes the fix for concurrent request
  defaults in [GHSA-g3f9-hcw7-r9w2](https://github.com/getkin/kin-openapi/security/advisories/GHSA-g3f9-hcw7-r9w2).
  Generator and application share that pin.
  See the [parser release](https://github.com/getkin/kin-openapi/releases/tag/v0.153.0).
- Semantic tests compile the exported documents with
  santhosh-tekuri/jsonschema/v6 v6.0.3 and enable format assertions.

## Export contract

Required properties and nullable values are independent. A nullable schema
accepts either its entire original schema or JSON null, including Core's nullable
`allOf` references. Enums, oneOf/anyOf/allOf, negation, arrays, typed maps,
open/closed objects, patterns, and size and numeric bounds retain their
constraints. Exclusive bounds become numeric. Supported formats are date-time,
email, uuid and uri. Consumers must enable format assertions to enforce them.

Only reachable local schema references enter `$defs`. Private extensions,
readOnly/writeOnly schemas, discriminators, external or missing references,
reference siblings and unknown keywords or formats cause export to fail.
Directional fields and unsupported constructs require a reviewed catalog
mapping. A successful conversion establishes schema compatibility only.
The catalog owns parameter merging, response variants, tool naming and
authorization under [issue 13](https://github.com/Braidplane/braidplane-core/issues/13).

## Local checks

```sh
task generate-api
bash scripts/go-toolchain.sh test -short ./api-schema/... ./pkg/publicschema ./cmd/clischema
bash scripts/go-toolchain.sh test -short ./transport/http ./cli/... ./frontend/... ./service/datasourcessvc/connector/http
task docs-check
```

The environment creation request and environment result exercise the embedded
source against REST and standalone JSON Schema validators. Synthetic tests
cover union membership, reference isolation and unsupported exports. Regenerate
the server, frontend Go client and projected CLI together with `task generate-api`.
