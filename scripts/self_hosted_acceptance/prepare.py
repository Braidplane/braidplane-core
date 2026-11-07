"""Create a disposable self-hosted acceptance fixture; never replace existing keys."""
import argparse
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess

SOURCE = Path(__file__).resolve().parent
ROOT = SOURCE.parent.parent


def openssl(directory, *arguments):
    subprocess.run(["openssl", *arguments], cwd=directory, check=True, capture_output=True)


def write_private(path, content):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w") as output:
        output.write(content)


def prepare(directory):
    directory = Path(directory).resolve()
    directory.mkdir(mode=0o700, parents=True, exist_ok=False)
    # Generated private keys stay private even if preparation stops midway.
    previous_mask = os.umask(0o077)
    try:
        (directory / "ca.cnf").write_text(
            "[req]\ndistinguished_name=dn\nx509_extensions=v3_ca\nprompt=no\n"
            "[dn]\nCN=Synthetic Customer CA\n[v3_ca]\n"
            "basicConstraints=critical,CA:true\nkeyUsage=critical,keyCertSign,cRLSign\n"
            "subjectKeyIdentifier=hash\n"
        )
        openssl(directory, "req", "-x509", "-newkey", "ec", "-pkeyopt",
                "ec_paramgen_curve:P-256", "-nodes", "-days", "2", "-config", "ca.cnf",
                "-keyout", "ca.key", "-out", "ca.pem")
        openssl(directory, "req", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
                "-nodes", "-subj", "/CN=database.customer.test", "-keyout", "server.key",
                "-out", "server.csr")
        (directory / "extensions.cnf").write_text(
            "subjectAltName=DNS:database.customer.test,DNS:mail.customer.test,DNS:app.customer.test\n"
            "extendedKeyUsage=serverAuth\nsubjectKeyIdentifier=hash\n"
            "authorityKeyIdentifier=keyid,issuer\nbasicConstraints=critical,CA:false\n"
            "keyUsage=critical,digitalSignature\n"
        )
        openssl(directory, "x509", "-req", "-in", "server.csr", "-CA", "ca.pem",
                "-CAkey", "ca.key", "-CAcreateserial", "-days", "2",
                "-extfile", "extensions.cnf", "-out", "server.pem")
        operator, migrator, runtime = [secrets.token_hex(24) for _ in range(3)]
        write_private(directory / "database.env",
                      f"POSTGRES_USER=customer_operator\nPOSTGRES_PASSWORD={operator}\nPOSTGRES_DB=braidplane\n")
        write_private(directory / "init.sql",
                      f"CREATE ROLE customer_migrator LOGIN PASSWORD '{migrator}';\n"
                      f"CREATE ROLE customer_runtime LOGIN PASSWORD '{runtime}';\n"
                      "ALTER DATABASE braidplane OWNER TO customer_migrator;\n"
                      "ALTER SCHEMA public OWNER TO customer_migrator;\n")
        environment = {
            "CONFIG_PATH": "/fixture/config.toml", "DEPLOYMENT_PROFILE": "self-hosted",
            "DB_HOST": "database.customer.test", "DB_PORT": "5432", "DB_USER": "customer_runtime",
            "DB_PASSWORD": runtime, "DB_DATABASE": "braidplane", "DB_SSL_MODE": "verify-full",
            "DB_SSL_ROOT_CERT": "/fixture/ca.pem", "SERVER_PORT": "3001",
            "SESSION_COOKIE_SECURE": "true", "FRONTEND_SESSION_COOKIE_SECURE": "true",
            "SERVER_DRAIN_TIMEOUT": "1s", "FRONTEND_SERVER_DRAIN_TIMEOUT": "1s",
            "LOG_LEVEL": "info", "LOG_HTTP_REQUESTS": "false", "LOG_ENVIRONMENT": "self-hosted-fixture",
            "FRONTEND_URL": "https://app.customer.test", "CORS_ALLOWED_ORIGINS": "https://app.customer.test",
            "GOOGLE_REDIRECT_URI": "https://app.customer.test/api/auth/google/callback",
            "SMTP_HOST": "mail.customer.test:1025", "SMTP_INSECURE_SKIP_VERIFY": "false",
            "SMTP_CA_CERT_FILE": "/fixture/ca.pem", "SMTP_NO_REPLY_ADDRESS": "recovery@customer.test",
            "ENCRYPTION_KEY_HEX": secrets.token_hex(32), "EXECUTION_REQUEST_KEY_HEX": secrets.token_hex(32),
            "SSL_CERT_FILE": "/fixture/ca.pem", "METRICS_ENABLED": "false",
            "SELF_SERVICE_REGISTRATION_ENABLED": "false", "ALLOW_PRIVATE_DATA_SOURCE_HOSTS": "false",
            "EXECUTION_BACKEND": "poller",
        }
        write_private(directory / "runtime.env", "".join(f"{key}={value}\n" for key, value in environment.items()))
        environment.update(DB_USER="customer_migrator", DB_PASSWORD=migrator)
        write_private(directory / "migrator.env", "".join(f"{key}={value}\n" for key, value in environment.items()))
        passwords = {"initial": secrets.token_hex(24), "replacement": secrets.token_hex(24)}
        write_private(directory / "passwords.json", json.dumps(passwords))
        write_private(directory / "bootstrap-password", passwords["initial"] + "\n")
        write_private(directory / "cli.env", "")
        for name in ("config.toml", "Caddyfile", "recovery.py", "frontend_security.py", "network.py", "cli_token.py", "signup.compose.yml", "signup.py", "closed.py", "browser.cjs", "trace.compose.yml", "traffic.py", "providers.py"):
            shutil.copyfile(SOURCE / name, directory / name)
        template = (SOURCE / "compose.yml.tmpl").read_text()
        grants = str(ROOT / "scripts/self-hosted-runtime-grants.sql") + ":/fixture/grants.sql:ro"
        (directory / "compose.yml").write_text(template.replace('"@GRANTS@"', json.dumps(grants)).replace('"@NODEMODULES@"', json.dumps(str(ROOT / 'frontend/node_modules') + ':/repo/frontend/node_modules:ro')))
        # Non-root application images read public certificates and configuration via individual mounts.
        for name in ("ca.pem", "server.pem", "config.toml"):
            (directory / name).chmod(0o644)
    finally:
        os.umask(previous_mask)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    prepare(parser.parse_args().directory)
    print("Prepared isolated fixture; generated secrets remain in its protected directory.")
