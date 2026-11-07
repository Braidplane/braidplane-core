#!/usr/bin/env bash
# Isolated Mailpit identity; this never installs a host trust root.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_dir="$repo_root/docker/mailpit"
mkdir -p "$output_dir"
lock_dir="$output_dir/.tls-lock"
if ! mkdir "$lock_dir" 2>/dev/null; then
    echo "Mailpit TLS generation is already locked: $lock_dir" >&2
    exit 1
fi
work_dir=""
cleanup() {
    if [[ -n "$work_dir" ]]; then rm -rf "$work_dir"; fi
    rmdir "$lock_dir"
}
trap cleanup EXIT

key="$output_dir/mail.example.com-key.pem"
cert="$output_dir/mail.example.com-cert.pem"
if [[ -e "$key" || -L "$key" || -e "$cert" || -L "$cert" ]]; then
    echo "Refusing to replace an existing Mailpit TLS identity or partial pair" >&2
    exit 1
fi
umask 077
work_dir="$(mktemp -d "$output_dir/.tls.XXXXXX")"
cat > "$work_dir/openssl.cnf" <<'CONFIG'
[req]
prompt = no
distinguished_name = subject
x509_extensions = extensions
[subject]
CN = mail.example.com
[extensions]
subjectAltName = DNS:mail.example.com,DNS:localhost,IP:127.0.0.1
CONFIG
if ! openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
    -config "$work_dir/openssl.cnf" -keyout "$work_dir/key.pem" -out "$work_dir/cert.pem" 2>"$work_dir/openssl.log"; then
    cat "$work_dir/openssl.log" >&2
    exit 1
fi
chmod 644 "$work_dir/cert.pem"
mv "$work_dir/key.pem" "$key"
mv "$work_dir/cert.pem" "$cert"
echo "Created isolated Mailpit TLS fixture in $output_dir"
