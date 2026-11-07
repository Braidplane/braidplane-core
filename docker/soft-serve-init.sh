#!/bin/sh

set -eu

DATA_PATH="${SOFT_SERVE_DATA_PATH:-/soft-serve}"
CONFIG_PATH="${DATA_PATH}/config.yaml"
SSH_KEY="${SOFT_SERVE_INITIAL_ADMIN_KEYS:-}"

trim_wrapping_quotes() {
  value="$1"

  case "$value" in
    \"*\")
      value=${value#\"}
      value=${value%\"}
      ;;
  esac

  case "$value" in
    \'*\')
      value=${value#\'}
      value=${value%\'}
      ;;
  esac

  printf '%s' "$value"
}

mkdir -p "$DATA_PATH"

if [ ! -f "$CONFIG_PATH" ]; then
  cat > "$CONFIG_PATH" <<'EOF'
name: "Soft Serve"
log_format: "text"
ssh:
  listen_addr: ":23231"
http:
  listen_addr: ":23232"
git:
  listen_addr: ":9418"
stats:
  listen_addr: ":23233"
EOF

  SSH_KEY=$(trim_wrapping_quotes "$SSH_KEY")
  if [ -n "$SSH_KEY" ]; then
    {
      printf 'initial_admin_keys:\n'
      printf '  - "%s"\n' "$SSH_KEY"
    } >> "$CONFIG_PATH"
  fi
fi

exec /usr/local/bin/soft serve
