#!/usr/bin/env bash
set -euo pipefail

task_dir=$(mktemp -d "${TMPDIR:-/tmp}/braidplane-oapi.XXXXXXXX")
trap 'rm -rf "$task_dir"' EXIT

bash scripts/go-toolchain.sh run ./cmd/communityschema api-schema/schema.yml > "$task_dir/public.json"
bash scripts/go-toolchain.sh tool -modfile=tools/go.mod oapi-codegen -config api-schema/oapi.yml "$task_dir/public.json"
