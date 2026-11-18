"""Prepare a new single-host bundle using customer TLS and mail settings."""
import argparse
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import shutil

from .release import compose_inputs, read_manifest

SOURCE = Path(__file__).resolve().parent


def prepare(directory, manifest, hostname, bind_address, smtp_host, sender, certificates, api_only=False, proxy_subnet=None):
    # Restrict customer text before writing Compose, dotenv, Caddy or SQL inputs.
    if not re.fullmatch(r"[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?", hostname) or "." not in hostname or hostname.endswith(".localhost"):
        raise ValueError("hostname must be a customer DNS name without a scheme or port")
    if ipaddress.ip_address(bind_address).version != 4:
        raise ValueError("bind address must be an IPv4 address")
    if proxy_subnet is None:
        base = int(ipaddress.IPv4Address("10.254.0.0"))
        network = ipaddress.IPv4Network((base + 8 * secrets.randbelow(8192), 29))
    else:
        network = ipaddress.IPv4Network(proxy_subnet)
        private_ranges = ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")
        if network.prefixlen != 29 or not any(network.subnet_of(ipaddress.IPv4Network(cidr)) for cidr in private_ranges):
            raise ValueError("proxy subnet must be a private IPv4 /29")
    if not re.fullmatch(r"[a-zA-Z0-9.-]+:[0-9]{1,5}", smtp_host) or not 1 <= int(smtp_host.rsplit(":", 1)[1]) <= 65535:
        raise ValueError("SMTP host must be hostname:port")
    if not re.fullmatch(r"[a-zA-Z0-9._+-]+@[a-zA-Z0-9.-]+", sender):
        raise ValueError("sender must be a customer email address")
    release = read_manifest(manifest)
    certificates = Path(certificates)
    names = ("ca.pem", "database.pem", "database.key", "edge.pem", "edge.key")
    for name in names:
        path = certificates / name
        if path.is_symlink() or not path.is_file() or not path.stat().st_size:
            raise ValueError(f"certificate input must be a nonempty regular file: {name}")
    directory = Path(directory)
    directory.mkdir(mode=0o700, parents=True, exist_ok=False)
    mask = os.umask(0o077)
    try:
        def write(name, content):
            with (directory / name).open("x") as output:
                output.write(content)
        def env(name, values):
            write(name, "".join(f"{key}={value}\n" for key, value in values.items()))
        (directory / "certificates").mkdir(mode=0o700)
        for name in names:
            shutil.copyfile(certificates / name, directory / "certificates" / name)
            (directory / "certificates" / name).chmod(0o600 if name.endswith(".key") else 0o644)
        for name in ("compose.json", "Caddyfile", "execution-grants.sql"):
            shutil.copyfile(SOURCE / name, directory / name)
        compose_path = directory / "compose.json"
        compose = json.loads(compose_path.read_text())
        compose["networks"]["proxy"]["ipam"]["config"][0]["subnet"] = str(network)
        services = compose["services"]
        edge_ip, frontend_ip, api_ip = (str(network[index]) for index in (2, 3, 4))
        for name, address in (("edge", edge_ip), ("frontend", frontend_ip), ("api", api_ip)):
            services[name]["networks"]["proxy"]["ipv4_address"] = address
        services["frontend"]["environment"]["FRONTEND_SERVER_TRUSTED_PROXIES"] = edge_ip
        services["api"]["environment"]["SERVER_TRUSTED_PROXIES"] = edge_ip if api_only else frontend_ip
        if api_only:
            del services["frontend"]
            services["edge"]["depends_on"] = {"api": {"condition": "service_healthy"}}
            caddy = directory / "Caddyfile"
            caddy.write_text(caddy.read_text().replace("reverse_proxy frontend:3002", "reverse_proxy api-proxy:3001"))
        compose_path.write_text(json.dumps(compose, indent=2) + "\n")
        shutil.copyfile(SOURCE.parent / "self-hosted-runtime-grants.sql", directory / "runtime-grants.sql")
        write("release.json", json.dumps(release, indent=2) + "\n")
        write("release.env", compose_inputs(release) + f"BRAIDPLANE_HOST={hostname}\nBRAIDPLANE_BIND_ADDRESS={bind_address}\nBRAIDPLANE_PROJECT_NAME=braidplane-{secrets.token_hex(6)}\n")
        passwords = {role: secrets.token_hex(32) for role in ("operator", "migrator", "runtime", "executor")}
        env("database.env", {"POSTGRES_USER": "braidplane_operator", "POSTGRES_PASSWORD": passwords["operator"], "POSTGRES_DB": "braidplane"})
        write("init.sql", "".join(f"CREATE ROLE braidplane_{role} LOGIN PASSWORD '{passwords[role]}';\n" for role in ("migrator", "runtime", "executor")) + "ALTER DATABASE braidplane OWNER TO braidplane_migrator;\nALTER SCHEMA public OWNER TO braidplane_migrator;\n")
        common = {"DB_HOST": "database", "DB_PORT": "5432", "DB_DATABASE": "braidplane", "DB_SSL_MODE": "verify-full", "DB_SSL_ROOT_CERT": "/etc/braidplane/ca.pem", "DEPLOYMENT_PROFILE": "self-hosted"}
        envelope_key = secrets.token_hex(32)
        for role in ("migrator", "runtime", "executor"):
            values = dict(common, DB_USER=f"braidplane_{role}", DB_PASSWORD=passwords[role])
            if role != "migrator":
                values.update(EXECUTION_BACKEND="poller", EXECUTION_REQUEST_KEY_HEX=envelope_key)
            env(role + ".env", values)
        # Keep the application encryption key out of the executor input.
        with (directory / "runtime.env").open("a") as output:
            output.write(f"ENCRYPTION_KEY_HEX={secrets.token_hex(32)}\n")
        config = f'''frontend_url="https://{hostname}"
backend_api_url="http://api:3001"
documentation_url=""
[deployment]
profile="self-hosted"
[server]
port=3001
drain_timeout="1s"
[frontend_server]
drain_timeout="1s"
[session]
cookie_secure=true
[frontend_session]
cookie_secure=true
[logging]
level="info"
log_http_requests=false
environment="self-hosted"
backend_file=""
frontend_file=""
[security]
allow_private_data_source_hosts=false
[auth]
self_service_registration_enabled=false
google_redirect_uri="https://{hostname}/api/auth/google/callback"
[cors]
allowed_origins=["https://{hostname}"]
[smtp]
host="{smtp_host}"
no_reply_address="{sender}"
insecure_skip_verify=false
user=""
password=""
'''
        write("config.toml", config)
        write("executor.toml", '[deployment]\nprofile="self-hosted"\n[server]\nport=3001\ndrain_timeout="1s"\n[logging]\nlevel="info"\nlog_http_requests=false\nbackend_file=""\n[security]\nallow_private_data_source_hosts=false\n')
        # Mounted public configuration must be readable by UID 65532.
        for name in ("config.toml", "executor.toml"):
            (directory / name).chmod(0o644)
    finally:
        os.umask(mask)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("directory", "manifest", "hostname", "bind_address", "smtp_host", "sender", "certificates"):
        parser.add_argument(name)
    parser.add_argument("--api-only", action="store_true", help="Omit the frontend and route TLS directly to the API.")
    parser.add_argument("--proxy-subnet", help="Use a non-overlapping private IPv4 /29 for trusted proxy traffic.")
    args = parser.parse_args()
    try:
        prepare(**vars(args))
    except (ValueError, OSError) as error:
        parser.exit(1, f"bundle preparation failed: {error}\n")
    print("Prepared new bundle. Preserve its protected credentials and keys with its database.")


if __name__ == "__main__":
    main()
