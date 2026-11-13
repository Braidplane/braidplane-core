#!/usr/bin/env bash
# gen-bruno-env.sh
#
# Writes per-collection Bruno "Worktree" environment files so the Bruno GUI
# uses the correct ports for the current worktree.
#
# Reads ports and mock URLs from the calling shell only.
#
# Usage:
#   scripts/gen-bruno-env.sh [target-dir]
#
# target-dir defaults to the current working directory (i.e. the repo root).
set -euo pipefail

TARGET_DIR="${1:-.}"

BASE_URL="http://127.0.0.1:${SERVER_PORT:-3001}/api"
MAILPIT_URL="http://localhost:${MAILPIT_WEB_PORT:-38025}"
HTTP_CRUD_URL="http://localhost:${HTTP_CONNECTOR_PORT:-38080}"
OPENAPI_SERVER_URL="http://localhost:${OPENAPI_SERVER_PORT:-38081}/schema.yml"
HUBSPOT_MOCK_URL="${HUBSPOT_BASE_URL:-http://localhost:${HUBSPOT_MOCK_PORT:-38082}}"
BIGQUERY_MOCK_URL="${BIGQUERY_MOCK_URL:-http://localhost:${BIGQUERY_MOCK_PORT:-38084}}"
DB_PORT_VAL="${DB_PORT:-35432}"
POSTGRES_PORT="${POSTGRES_CONNECTOR_PORT:-35433}"
MYSQL_PORT="${MYSQL_CONNECTOR_PORT:-33307}"
MONGO_PORT="${MONGO_CONNECTOR_PORT:-37017}"
FTP_PORT="${FTP_CONNECTOR_PORT:-32121}"
S3_CONNECTOR_URL="http://localhost:${S3_CONNECTOR_PORT:-39000}"
GIT_SSH_PORT_VAL="${GIT_SSH_PORT:-32223}"

write_env() {
  local file="$1"
  local content="$2"
  mkdir -p "$(dirname "$file")"
  printf '%s' "$content" > "$file"
}

write_env "$TARGET_DIR/api-schema/bruno/setup-flow/environments/Worktree.bru" \
"vars {
  baseUrl: $BASE_URL
  mailpitUrl: $MAILPIT_URL
}
"

write_env "$TARGET_DIR/api-schema/bruno/projects/environments/Worktree.bru" \
"vars {
  baseUrl: $BASE_URL
  mailpitUrl: $MAILPIT_URL
}
"

write_env "$TARGET_DIR/api-schema/bruno/Braidplane API/environments/Worktree.bru" \
"vars {
  baseUrl: $BASE_URL
  mailpitUrl: $MAILPIT_URL
  httpcrudUrl: $HTTP_CRUD_URL
  frontendUrl: ${FRONTEND_URL:-http://localhost:${FRONTEND_SERVER_PORT:-3002}}
  postgresPort: $POSTGRES_PORT
  mysqlPort: $MYSQL_PORT
  mongoPort: $MONGO_PORT
}
"

write_env "$TARGET_DIR/api-schema/bruno/multi-tenant-isolation/environments/Worktree.bru" \
"vars {
  baseUrl: $BASE_URL
  mailpitUrl: $MAILPIT_URL
  pipelineProjectSlug: pipeline-workspace
  prodEnvironmentSlug: dev
  postgresPort: $DB_PORT_VAL
}
"

# Data-source verification runs against the connector fixture databases, not the
# Braidplane application database used by multi-tenant isolation.
write_env "$TARGET_DIR/api-schema/bruno/data-sources-verification/environments/Worktree.bru" \
"vars {
  baseUrl: $BASE_URL
  mailpitUrl: $MAILPIT_URL
  httpcrudUrl: $HTTP_CRUD_URL
  openapiServerUrl: $OPENAPI_SERVER_URL
  hubspotMockUrl: $HUBSPOT_MOCK_URL
  bigqueryMockUrl: $BIGQUERY_MOCK_URL
  pipelineProjectSlug: pipeline-workspace
  prodEnvironmentSlug: prod
  postgresPort: $POSTGRES_PORT
  mysqlPort: $MYSQL_PORT
  mongoPort: $MONGO_PORT
  ftpPort: $FTP_PORT
  s3ConnectorUrl: $S3_CONNECTOR_URL
  gitSSHPort: $GIT_SSH_PORT_VAL
}
"

echo "Bruno 'Worktree' environment written for all collections (target: $TARGET_DIR)."
echo "Select 'Worktree' in the Bruno GUI."
