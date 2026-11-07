#!/bin/sh
set -eu

# Readiness proves authenticated access and fixture contents, even after startup.
if [ "${1:-}" = check ]; then
  readback=$(mktemp)
  trap 'rm -f "$readback"' EXIT
  aws s3api get-object --bucket braidplane-fixtures --key "$S3_FIXTURE_KEY" "$readback" >/dev/null
  test "$(sha256sum < "$S3_FIXTURE_SOURCE")" = "$(sha256sum < "$readback")"
  exit 0
fi

# The single-process server creates the bucket asynchronously on a fresh volume.
attempt=0
until aws s3api head-bucket --bucket braidplane-fixtures >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    echo "S3 fixture bucket did not become ready" >&2
    exit 1
  fi
  sleep 2
done
aws s3api put-object --bucket braidplane-fixtures --key "$S3_FIXTURE_KEY" --body "$S3_FIXTURE_SOURCE" >/dev/null
sh "$0" check
