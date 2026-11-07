#!/usr/bin/env python3
"""Run the local application while retaining its database and encryption keys."""
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def load_keys(path, database_exists):
    if path.is_symlink():
        raise ValueError("Local key file must not be a symbolic link")
    if not path.exists():
        if database_exists:
            raise ValueError("Database already exists but its key file is missing; restore the original keys")
        with open(path, "x", opener=lambda name, flags: os.open(name, flags, 0o600)) as output:
            for name in ("ENCRYPTION_KEY_HEX", "EXECUTION_REQUEST_KEY_HEX"):
                output.write(f"export {name}={os.urandom(32).hex()}\n")
    matches = re.fullmatch(r"export ENCRYPTION_KEY_HEX=([0-9a-f]{64})\nexport EXECUTION_REQUEST_KEY_HEX=([0-9a-f]{64})\n", path.read_text())
    if not matches:
        raise ValueError("Invalid local key file; restore it rather than generating replacement keys")
    if path.stat().st_mode & 0o077:
        raise ValueError("Local key file permissions must be 0600")
    return dict(zip(("ENCRYPTION_KEY_HEX", "EXECUTION_REQUEST_KEY_HEX"), matches.groups()))


def main():
    os.chdir(ROOT)
    state = ROOT / ".worktree/community"
    state.mkdir(parents=True, exist_ok=True)
    env = os.environ | {
        "COMPOSE_PROJECT_NAME": os.environ.get("COMPOSE_PROJECT_NAME", "braidplane-community"),
        "DB_HOST": "127.0.0.1", "DB_PORT": os.environ.get("DB_PORT", "35432"),
        "DB_USER": "postgres", "DB_PASSWORD": "postgres", "DB_DATABASE": "braidplane", "DB_SSL_MODE": "disable",
        "DEPLOYMENT_PROFILE": "local", "CONFIG_PATH": str(ROOT / "config.toml"),
        "SELF_SERVICE_REGISTRATION_ENABLED": os.environ.get("SELF_SERVICE_REGISTRATION_ENABLED", "true"),
        "SERVER_HOST": "127.0.0.1", "SERVER_PORT": os.environ.get("SERVER_PORT", "3001"),
        "FRONTEND_SERVER_HOST": "127.0.0.1", "FRONTEND_SERVER_PORT": os.environ.get("FRONTEND_SERVER_PORT", "3002"),
        "MAILPIT_SMTP_PORT": os.environ.get("MAILPIT_SMTP_PORT", "31025"),
        "MAILPIT_WEB_PORT": os.environ.get("MAILPIT_WEB_PORT", "38025"),
    }
    env["FRONTEND_URL"] = "http://127.0.0.1:" + env["FRONTEND_SERVER_PORT"]
    env["BACKEND_API_URL"] = "http://127.0.0.1:" + env["SERVER_PORT"]
    env["SMTP_HOST"] = "127.0.0.1:" + env["MAILPIT_SMTP_PORT"]
    worker_port = os.environ.get("COMMUNITY_WORKER_PORT", "3003")
    ports = [env[name] for name in ("DB_PORT", "SERVER_PORT", "FRONTEND_SERVER_PORT", "MAILPIT_SMTP_PORT", "MAILPIT_WEB_PORT")] + [worker_port]
    if len(set(ports)) != len(ports) or any(not port.isdecimal() or not 0 < int(port) < 65536 for port in ports):
        raise ValueError("Local services require distinct valid ports")
    existing = subprocess.check_output(["docker", "compose", "ps", "--all", "--quiet", "database"], env=env, text=True).strip()
    env.update(load_keys(state / "keys.env", bool(existing)))
    cert = ROOT / "docker/mailpit/mail.example.com-cert.pem"
    key = ROOT / "docker/mailpit/mail.example.com-key.pem"
    if not cert.exists() or not key.exists():
        subprocess.run(["bash", "scripts/generate-email-cert.sh"], check=True, env=env)
    subprocess.run(["task", "build"], check=True, env=env)
    subprocess.run(["docker", "compose", "up", "-d", "--wait", "database", "email-mailpit"], check=True, env=env)
    subprocess.run(["task", "migrate-run"], check=True, env=env)
    processes, logs = [], []
    def interrupt(_signum, _frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupt)
    try:
        for role, command, overrides in (
            ("api", ["out/braidplane-server", "api"], {}),
            ("worker", ["out/braidplane-server", "worker"], {"SERVER_PORT": worker_port}),
            ("frontend", ["out/frontend"], {}),
        ):
            output = (state / (role + ".log")).open("a")
            logs.append(output)
            processes.append(subprocess.Popen(command, env=env | overrides, stdout=output, stderr=subprocess.STDOUT))
        pending = {env["BACKEND_API_URL"] + "/readyz", "http://127.0.0.1:" + worker_port + "/readyz", env["FRONTEND_URL"] + "/auth/login"}
        deadline = time.monotonic() + 60
        while True:
            if any(process.poll() is not None for process in processes):
                raise RuntimeError("A local service exited; see .worktree/community/*.log")
            for url in list(pending):
                try:
                    with urllib.request.urlopen(url, timeout=1) as response:
                        if response.status == 200:
                            pending.remove(url)
                except OSError:
                    pass
            if pending and time.monotonic() >= deadline:
                raise RuntimeError("Local services did not become ready; see .worktree/community/*.log")
            if not pending and deadline:
                print(f"Application: {env['FRONTEND_URL']}\nMail: http://127.0.0.1:{env['MAILPIT_WEB_PORT']}\nCtrl-C stops application processes; database, mail and keys are retained.", flush=True)
                deadline = 0
            time.sleep(1)
    finally:
        for process in processes:
            if process.poll() is None:
                process.terminate()
        for process in processes:
            try:
                process.wait(timeout=40)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        for output in logs:
            output.close()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
    except (ValueError, RuntimeError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))
