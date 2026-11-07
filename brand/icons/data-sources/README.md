# Braidplane data-source icons

The frontend serves this library at `/assets/icons/data-sources/`. Use these
canonical paths rather than copying artwork into `frontend/assets/`.

## Delivered assets

- `generic/`: Lucide Static v1.23.0 workflow and protocol icons, with ISC and
  Feather-derived MIT notices in `licenses/lucide-LICENSE`.
- `vendors/brand/git.svg`: the official, unmodified Git orange logomark by Jason
  Long, under CC BY 3.0 and the Git trademark policy.
- `vendors/brand/mongodb.png`: the official, unmodified MongoDB leaf, used for
  factual identification under MongoDB's Trademark Standards. Its artwork is
  not relicensed under this repository's Apache-2.0 grant or Simple Icons CC0.

[Vendor metadata](vendors/metadata.json) records the exact source, SHA-256,
available checked dates and use conditions. [Release decisions](RELEASE_REVIEW.md) explain
retained marks and unresolved conditions. `licenses/vendor-NOTICE.txt` supplies
attribution; `licenses/git-CC-BY-3.0.txt` retains the Git artwork license.
Simple Icons notices remain for the provisionally retained derived artwork.

Seven existing connector logos are also retained provisionally by the owner:
PostgreSQL, MySQL, Google Drive, BigQuery, Snowflake, HubSpot and GitHub.
Their exact bytes and unresolved conditions are recorded in metadata and the
release review. Their retention does not relicense them under Apache-2.0.

## Reuse

The delivered vendor marks identify integrations in the existing product UI.
Keep their artwork unchanged, retain notices and registration symbols, and
keep Braidplane the product brand. A neutral white backing for the dark MongoDB
leaf changes its presentation without changing its pixels.

Review changed uses against current vendor terms and any applicable recorded
restrictions. The icon library does not grant general marketing, partnership,
certification or endorsement rights. Unused marks and recoloured monochrome
variants are absent from this source tree and its frontend image.

## Preview

Open `preview.html` directly or visit `/assets/icons/data-sources/preview.html`
in a local frontend runner. It shows the delivered assets with attribution;
a preview is not promotional approval.
