# ODCS support and enforcement

Braidplane Core stores Open Data Contract Standard (ODCS) v3.2.0 documents
in a project catalog. A revision can include terms the pipeline never
evaluates. Inspect the revision to see what maps to a flat pipeline port.
Open the run details to see which checks ran.

## Document admission and preservation

Core validates the whole document offline against the embedded
[Bitol v3.2.0 schema at commit `f0bdad9`](https://github.com/bitol-io/open-data-contract-standard/blob/f0bdad95346905d500be5ef4b2c2d9b1d95223b7/schema/odcs-json-schema-v3.2.0.json).
Core keeps the vendored schema intact and applies four corrections in memory. It
rejects Unicode whitespace in stable IDs, allows schema objects to declare
array roots, accepts `file:///` references as text, and allows unresolved
variable tokens in applicable string positions.

Record a JSON document with `createODCSRevision` or
`braidplane contract revision create orders --project etl --file orders.json`.
Git artifact import also accepts supported YAML under `contracts/`.
Admission requires `kind: DataContract`, `apiVersion: v3.2.0`, a document
`id` matching the catalog identity, a document `version`, and a `schema`
declaration. You can record an empty schema or an object without an ID.
To bind an object to a port, give it a literal ID. Core checks for duplicate
declaration IDs in applicable arrays, incompatible or duplicate enum
literals, and composite relationship sides of different lengths.

Core records admitted declarations as canonical JSON, including opaque vendor
values. Formatting and key order do not affect the SHA-256 digest. Numeric
literal spellings can affect it. Catalog writes reject invalid Unicode,
duplicate JSON keys, trailing documents, and nesting beyond 64 levels.
The 1 MiB limit applies to both the input and the canonical JSON. Numeric
literals can be up to 1,024 bytes, with exponents from −4096 to 4096.
Core checks recognized secret-bearing keys inside opaque values too.
Use a simple unresolved token such as `${SECRET}` in those positions.

The pinned conformance corpus contains source-linked positive and negative
examples. `TestPinnedUpstreamConformance` checks admission,
declaration round trips and digest stability. The same suite checks the
normative rules and malformed declarations. `TestCoverageAccountsForEveryNodeInAdmittedConformanceCorpus`
checks coverage of the admitted examples. [Conformance corpus][corpus] ·
[Admission tests][admission] · [Recording tests][record] ·
[Git artifact parsing tests][artifacts] · [Coverage tests][coverage]

## Declaration support matrix

Core preserves every declaration it admits. An unsupported structural
declaration on the selected object can prevent Core from mapping that
object. Quality and SLA declarations can stay in the revision even when a
port mapping succeeds.

| Declaration | Flat port mapping | Explicit compiled projection | Runtime enforcement and representative tests |
|---|---|---|---|
| Schema objects, properties and presence | One object with at least one property. Field names must be unique, nonempty literal names without `.`, `[` or `]`. ODCS `required: true` maps to presence and non-null checks. Omitted or false makes a field optional and nullable. | New executable pins require a nonempty object at the root, using rows or JSON with a matching object root. Open objects allow extra fields. Nested properties and explicit object required-name lists can map. Standalone projections can also describe supported array roots. | Presence and null checks apply to evaluated data. JSON root and representation checks depend on the port. [Converter][flat] · [Rich compiler][rich] · [Executable pins][pins] · [Structural values][structures] |
| Logical types and nested structures | Maps `string`, `integer`, `number`, `boolean`, `object` and `array`. Bare objects and arrays carry a top-level type only. Nested `properties`, `items` and `map` block the flat shape. Missing logical types also block it. | Maps supported nested objects and arrays, maps with string keys, and fixed-dimension vectors with `float32` or `float64` elements. Arrays require item definitions. The mapping must advertise each required kind. Vector model and distance metadata stay advisory. | Rich structural checks recurse into objects, array items and map keys and values, and validate vector dimensions and element values. Flat query and sink row checks can leave a bare type unchecked when there is no applicable value rule. Transform row and JSON outputs check declared types. [Converter][flat] · [Query fixture][query] · [Transform checks][transform] · [Structural collections][structures] |
| Strings, enums and collection constraints | Maps string `minLength`, `maxLength`, `email` and `uuid` formats and type-compatible scalar enum values. Enum labels and descriptions are descriptive. | Maps string lengths, `email`, `uuid` and `url` formats, compatible scalar enums, and array `minItems`, `maxItems` and `uniqueItems`. An explicit zero `minProperties` is supported. | Applied rules check observed values, with uniqueness scoped to the items of the evaluated array. Patterns, `uri`, nonzero object minimums, object maximums and vector normalization requirements have no mapping in the applicable path. [Converter][flat] · [Flat rules][field-rules] · [Rich compiler][rich] · [Structural values][structures] |
| Numeric bounds and physical types | Maps inclusive `minimum` and `maximum`. Integer bounds and enums must fit signed 64-bit integers. Ordinary `number` bounds and enum values must be exactly representable as float64. A document-level `customProperties` entry with `vendor: braidplane` and `property: decimalPhysicalTypes` can map exact `physicalType` spellings to a precision from 1 to 38 and a scale from 0 to that precision. In decimal mode every selected `number` needs a mapping and must omit the numeric format. Other physical spellings alone are descriptive. | Supports `i8`, `i16`, `i32` (the integer default), `i64`, and `f32` or `f64`. A number needs an explicit format or physical mapping. Every explicit physical type must match the selected capability mapping. A compatible physical mapping can select exact decimals. | Evaluators check mapped numeric bounds, widths or decimal precision and scale within their respective port contracts. Exclusive bounds, `multipleOf` and unsupported widths or formats can block the mapping. [Exact flat literals][flat] · [Decimal mapping][decimal] · [Rich compiler][rich] · [Decimal values][decimal-values] · [Structural values][structures] |
| Temporal types | `date`, `time` and `timestamp` block flat conversion. | Maps date with `yyyy-MM-dd`, local time with `HH:mm:ss`, local timestamp with `yyyy-MM-ddTHH:mm:ss` and `timezone: false`, and an instant with `YYYY-MM-DDTHH:mm:ss.SSSZ` or `yyyy-MM-ddTHH:mm:ssZ` when timezone is enabled or omitted. Applicable default zones are limited to UTC and Etc/UTC. | Rich evaluation checks calendar, local-clock and offset semantics. Unsupported format or zone selections produce `temporal_mapping_unsupported`. [Converter][flat] · [Rich temporal mapping][rich] · [Structural values][structures] |
| Variables and defaults | Tokens remain in the document. Unresolved field names, types, options or enum values can block the selected flat shape with `variable_unresolved`. | Selected structural declarations and selected relationships can use explicit non-secret string inputs with source revisions or an authored fallback. Core expands once, revalidates, and records the resolved digest and compiler defaults. | Runtime uses the compiled shape. Catalog admission and compilation do not read process variables or credentials to expand ODCS tokens. [Admission][admission] · [Rich resolution][rich] · [Specialization][specialize] |
| Relationships, keys, uniqueness and partitioning | Relationships and `primaryKey`, `primaryKeyPosition`, `unique`, `partitioned` and `partitionKeyPosition` stay in the document without mapping to checks. | Explicitly selected relationships can compile to ordered key pairs between exact inputs. Core checks key paths, pairing and compatible key types. Reference text never fetches or selects an external document. | No foreign-key, key uniqueness or partition check follows from these declarations. Array `uniqueItems` is a separate structural rule. [Conformance][corpus] · [Relationship compiler tests][rich] · [Coverage][coverage] |
| Quality and SLA | Preserves schema- and property-level quality, including text, library, SQL and custom declarations, and document-level `slaProperties` and `slaDefaultElement`. Coverage reports `unsupported` for these declarations. | The compiler derives no quality queries, custom implementations, library rules, SLA metrics or SLA thresholds from these declarations. | No quality or SLA runtime verdict is available from ODCS declaration coverage, including for freshness, latency or delivery. [Conformance][corpus] · [Coverage][coverage] · [Proof claims][proof] |
| Governance, ownership and descriptive metadata | Preserves `context`, roles, servers and encoding, team, support, price, tags, tenant, domain, data product, descriptions, names, classification, examples, synonyms, deprecation, critical-data markers, semantic roles, authoritative definitions, physical names, encrypted names and vendor extensions. The flat shape can include field descriptions. | Retains metadata in the revision. Connector mappings, grants and runtime configuration come from Braidplane configuration and explicit selections. | No policy, access grant, provisioning, encryption or routing follows from these fields. ODCS transform descriptions, logic and source declarations never execute as pipeline transforms. [Conformance][corpus] · [Recording][record] · [Coverage][coverage] |

Flat field-level value rules require structured data at the boundary. File and
multipart form representations reject active field validation rules. CSV can
coerce supported scalar values for field rules where serialized rows are
available. [Field declaration tests][field-rules] · [CSV and sink checks][sink]

## Bind an exact revision to a pipeline

Use the contract slug, revision slug and schema object ID from the same project.
The document's `version` and the object name cannot substitute for these
identifiers. Core rejects `latest`. For a given port declaration, choose an
inline contract or a catalog reference. This query output uses a reference.

```yaml
output_contract_ref:
  contract_slug: orders
  revision_slug: v1
  schema_object_id: order
  representation: rows
```

You can also attach references to the output of an event input step, inputs
and outputs of transforms (including named inputs), and sink inputs. References
on saved operation ports apply in contract composition. Choose a representation
the port supports and supply the JSON root shape where required.

Core reads the named revision from the catalog and verifies its ODCS version
and canonical document digest. The digest comes from the revision. Core
stores it in the port pin along with the compiler identity, shape and
coverage. [Reference validation][refs] ·
[Flat snapshots][snapshots] · [Saved operation snapshots][operation-pins]

Flat execution reloads the retained pipeline specification and its pins without
consulting the catalog. Reload checks port identity, representation, compiler
and digest format. It rejects missing, duplicate or unmatched pins. Adding a
catalog revision leaves an existing pipeline version's checks unchanged. If a
selected declaration has no flat mapping, Core refuses flat execution. A
retained Git version can keep the authored reference for explicit compilation.

For rich execution, provide an exact projection selection to
`compilePipelineVersion` or use the CLI with the full retained version hash.

```sh
braidplane pipeline version compile orders-sync '<spec-hash>' \
  --project etl --projection-file projection.json
```

The compiled plan pins the catalog revision, object, digest, mapping and
structure. Select the plan by its digest when you deploy. Each run attempt
retains the identity it started with. On reload, Core verifies the stored
plan against its pins. A missing or mismatched rich pin blocks execution.
[Flat snapshots][snapshots] · [Rich pins][pins] ·
[Compiled identity checks][plan] · [Retained-version tests][retained]

## Inspect coverage and run evidence

```sh
braidplane contract revision inspect orders v1 --project etl --object order
braidplane contract revision inspect orders v1 --project etl --object order --shape-only
```

The first command returns the revision, flat coverage and diagnostics. It
includes a shape if Core can map the object. The second command exports
that shape by itself and fails if there is no flat mapping for the selection.
Check compilation diagnostics for an explicit projection. Both commands call
`inspectODCSRevision`. [OpenAPI reference](../api/index.md) [API inspection tests][http] · [CLI inspection tests][cli]

- Admission can reject a document with `ODCS_DOCUMENT_INVALID`. Inspection
  of an admitted revision reports declaration paths without submitted values. Flat selection can return `object_not_found`,
  `shape_ambiguous`, `type_unsupported`, `mapping_unsupported`,
  `mapping_invalid` or a more specific decimal or variable diagnostic.
  Server-backed pipeline validation identifies the affected step and port and
  prefixes converter codes, for example `ODCS_TYPE_UNSUPPORTED`. Coverage
  exceeding 16,384 entries or 8 MiB returns `coverage_limit_exceeded` and
  withholds both the shape and partial coverage. [Converter][flat] ·
  [Coverage budgets][coverage] · [Pipeline diagnostics][snapshots]
- Flat inspection reports `enforced` for a rule mapped into the shape,
  `descriptive` for retained metadata, `unsupported` when a mapping or
  evaluator is absent, and `unresolved` when a selection or value is missing.
  The compiler uses `validated` for compiled structural declarations and
  selected relationship mappings. These states describe compilation.
  If structural mapping fails, Core withholds the executable shape or plan.
  [Flat coverage][coverage] · [Rich coverage and failures][rich]
- A static Proof receipt can record whether the exact revision is valid and
  whether its supported shape is compatible with the candidate port. Unsupported or unresolved declarations and runtime
  payload conformance remain unverified in the static receipt. A source schema
  observation is separate evidence. [ODCS Proof tests][proof]
- Read run evidence in the run details or with
  `braidplane pipeline run get <run-slug> --project <project> --output json`.
  Flat runtime failures can report contract violations without showing payload values. Composition
  attempts can also record `timeline.structural_checks` at event admission,
  producer output, normalized transform input and before sink dispatch. A
  recorded check identifies the attempt, port, phase and evaluated projection
  or contract. `passed` applies to the observed batch, which may be empty or
  truncated. `failed` records a contract violation. `error`, `skipped`,
  `unavailable` and absent evidence give no conformance verdict. Check evidence
  omits evaluated values and does not establish whole-dataset compliance or
  destination delivery. [Runtime journal tests][journal] · [Run CLI tests][run-cli]

## Upgrading the pin

An ODCS upgrade requires an explicit release decision and a fixed upstream
commit. Vendor the schema and license, record the schema's SHA-256, and check
each correction against the new specification. Rerun the conformance corpus
and the mapping, evaluator, and snapshot tests before updating the matrix and
diagnostics. Existing pins keep their compiler identity and meaning.
[Schema provenance][provenance]

[provenance]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/odcs/schema/README.md
[corpus]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/odcs/testdata/upstream-conformance.json
[admission]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/odcs/conformance_test.go
[record]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/odcs/document_test.go
[artifacts]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/odcs/artifact_test.go
[coverage]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/odcs/coverage_test.go
[flat]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/odcs/convert_test.go
[query]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/odcs/runtime_test.go
[rich]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/odcs/projection_test.go
[decimal]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/odcs/decimal_mapping_test.go
[decimal-values]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/pipeline/payload/decimal_test.go
[field-rules]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/pipeline/payload/validations_test.go
[structures]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/pipeline/payload/structure_test.go
[transform]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/pipeline/transform/sql/output_contract_test.go
[sink]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/pipeline/run/sink_input_contract_runtime_test.go
[specialize]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/contractplan/specialize_test.go
[refs]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/pipeline/validate/odcs_contract_ref_test.go
[snapshots]: https://github.com/Braidplane/braidplane-core/blob/main/service/pipelinesvc/odcs_contracts_test.go
[pins]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/contractplan/admission_test.go
[operation-pins]: https://github.com/Braidplane/braidplane-core/blob/main/service/contractplansvc/service_test.go
[plan]: https://github.com/Braidplane/braidplane-core/blob/main/pkg/contractplan/plan_test.go
[retained]: https://github.com/Braidplane/braidplane-core/blob/main/service/contractplansvc/e2e/rich_admission_test.go
[http]: https://github.com/Braidplane/braidplane-core/blob/main/transport/http/odcs_contracts_test.go
[cli]: https://github.com/Braidplane/braidplane-core/blob/main/cli/cmd/braidplane/contract_test.go
[proof]: https://github.com/Braidplane/braidplane-core/blob/main/service/proofreceiptsvc/odcs_test.go
[journal]: https://github.com/Braidplane/braidplane-core/blob/main/service/pipelineruntime/structural_checks_test.go
[run-cli]: https://github.com/Braidplane/braidplane-core/blob/main/cli/cmd/braidplane/pipeline_contract_evidence_test.go
