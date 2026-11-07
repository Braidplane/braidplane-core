# Vendor-mark release review

Reviewed on 4 November 2026. The owner elected to retain the seven existing
connector logos provisionally after reviewing Kestra and Airbyte's public
practice. Their presence in peer repositories is distribution precedent, not a
transfer of vendor rights. This decision supersedes the earlier local removal
candidate; it does not establish new vendor permission or general marketing use.

## Current distribution

The source tree and frontend image include nine marks selected by the connector
UI: PostgreSQL, MySQL, Google Drive, BigQuery, Snowflake, MongoDB, HubSpot, Git and
GitHub. Names and connector behavior are unchanged. The frontend copies
`brand/icons/` and serves it at `/assets/icons/`, including adjacent notices and
the preview. Unused vendor artwork and all recoloured monochrome variants remain
excluded from the current tree. Earlier local artifacts have different scopes.

[Metadata](vendors/metadata.json) records every delivered asset's source,
guidelines and SHA-256. Seven assets retain their previous bytes. Git uses the
unmodified official orange logomark by Jason Long, with CC BY 3.0 credit/license
and ™. MongoDB uses the unmodified official leaf linked from its standards,
with ® and a white backing for contrast. No Apache-2.0 or CC0 license is asserted
for vendor artwork merely because it is bundled here.

## Conditions still to reconcile for the retained seven

| Mark | Published evidence and unresolved scope |
|---|---|
| HubSpot | [Developer Terms section 6](https://legal.hubspot.com/hs-developer-terms) provides a conditional route for HubSpot-provided, unaltered marks for the developer Solution. Account/app applicability and official asset provenance remain unverified. A separate letter is not the only route. |
| MySQL | [Conditional-use rules](https://www.mysql.com/about/legal/trademark.html) cover specific clickable badges outside password-protected pages; they do not establish the standalone dolphin's authenticated connector-badge scope. |
| GitHub | The [toolkit](https://brand.github.com/foundations/logo) describes integration identification and also requires prior written permission and permitted artwork. Applicable permission for this file remains unverified. |
| PostgreSQL | The [policy](https://www.postgresql.org/about/policies/trademarks/) permits factual word references and specifies logo treatment. The basis for this particular graphical badge remains unresolved. |
| Google Drive | [Drive guidance](https://developers.google.com/workspace/drive/api/guides/branding) requires no pre-approval for compliant use. Its in-app examples describe Drive actions and stored files; the broader Workspace badge's scope remains to be reconciled. |
| BigQuery | The [Cloud library](https://cloud.google.com/icons) supplies product icons for diagrams and technical documentation. This connector-badge scope remains unverified. |
| Snowflake | The [design guide](https://www.snowflake.com/brand-guidelines/) specifies artwork treatment. It does not itself establish the exact distributed badge's permission. Separately recorded restrictions remain applicable. |

The owner's provisional retention is not final resolution of these conditions.
Track concrete conditions against the exact use; do not start a blanket vendor
permission-letter campaign. Existing promotional restrictions remain in force.

## Retained notices and further changes

`licenses/vendor-NOTICE.txt` retains attribution, ownership and non-affiliation.
`licenses/git-CC-BY-3.0.txt` contains the full Git artwork license. The generated
root notice inventory includes both. [Git's policy](https://git-scm.com/about/trademark)
and [MongoDB's standards](https://www.mongodb.com/legal/trademark-usage-guidelines)
provide their factual-identification routes and remaining display conditions.

The unused dbt, Google Cloud, Redis, Apache Kafka, RabbitMQ, ClickHouse,
Elasticsearch, MariaDB and SQLite artwork is absent from this tree. This changes
no separately recorded allowance or restriction. Private permissions, denials
and account evidence remain in their owning records. Recheck changed channels,
asset bytes, policies and stop/revocation instructions before making new uses.
