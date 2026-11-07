#!/usr/bin/env bash
set -euo pipefail

task_file=$(mktemp "${TMPDIR:-/tmp}/braidplane-public-oapi.XXXXXXXX")
trap 'rm -f "$task_file"' EXIT
bash scripts/go-toolchain.sh run ./cmd/communityschema api-schema/schema.yml > "$task_file"
bash scripts/go-toolchain.sh tool -modfile=tools/go.mod oapi-codegen -config api-schema/oapi-client.yml "$task_file"
