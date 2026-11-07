"""Opt-in database grant checks in an owned, network-isolated disposable container."""
import os
from pathlib import Path
import subprocess
import time
import unittest
import uuid

ROOT = Path(__file__).resolve().parent.parent
POSTGRES = "postgres:18.6@sha256:edb0c08dc33befd0262ab3b5cf1c4f3f8cfd8b09e88c05b45adee29d6761ddb6"


@unittest.skipUnless(os.environ.get("BRAIDPLANE_INSTALL_DATABASE_TEST") == "1", "requires explicit disposable Docker fixture")
class InstallationDatabaseTest(unittest.TestCase):
    def docker(self, *args, sql=None, check=True):
        return subprocess.run(["docker", *args], input=sql, capture_output=True, text=True, check=check)

    def psql(self, sql, check=True):
        return self.docker("exec", "-i", self.container, "psql", "-U", "postgres", "-d", "braidplane", "-v", "ON_ERROR_STOP=1", "-At", sql=sql, check=check)

    def test_executor_capabilities_and_denied_authority(self):
        self.container = "bp1322-grants-" + uuid.uuid4().hex[:12]
        self.docker("run", "--detach", "--name", self.container, "--network", "none",
                    "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "-e", "POSTGRES_DB=braidplane", POSTGRES)
        self.addCleanup(self.docker, "rm", "--force", "--volumes", self.container)
        for _ in range(60):
            if self.docker("exec", self.container, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", check=False).returncode == 0:
                break
            time.sleep(0.5)
        else:
            self.fail("disposable PostgreSQL did not become ready")
        self.psql("CREATE ROLE braidplane_migrator; CREATE ROLE braidplane_runtime; CREATE ROLE braidplane_executor; CREATE ROLE fixture_inherited;")
        catalog = (ROOT / "postgres/migrations/000001_core.up.sql").read_text()
        # The fixture grants the migration owner its real database/schema authority.
        self.psql("ALTER DATABASE braidplane OWNER TO braidplane_migrator; ALTER SCHEMA public OWNER TO braidplane_migrator;")
        self.psql("SET ROLE braidplane_migrator;\n" + catalog)
        self.psql("SET ROLE braidplane_migrator; CREATE TABLE public.core_schema_migrations(version bigint, dirty boolean);")
        def grants(path):
            sql = path.read_text()
            for name in ("runtime", "migrator", "executor"):
                sql = sql.replace(f":'{name}_role'", f"'braidplane_{name}'").replace(f':"{name}_role"', f'"braidplane_{name}"')
            return "SET ROLE braidplane_migrator;\n" + sql
        baseline = grants(ROOT / "scripts/self-hosted-runtime-grants.sql")
        execution = grants(ROOT / "scripts/installation/execution-grants.sql")
        self.psql(baseline)
        self.psql(execution)
        self.psql(execution)
        print("PostgreSQL fixture:", self.psql("SHOW server_version;").stdout.strip())
        for capability in (
            "claim_next_execution_request()", "heartbeat_execution_request(uuid,uuid)",
            "complete_execution_request(uuid,uuid,bytea,boolean)", "execution_request_metrics()",
            "connector_execution_admission_metrics()",
            "check_connector_dispatch(uuid,uuid,text,text,text,jsonb,text,timestamptz,boolean)",
            "snowflake_wif_ready(text,text,text,text)",
        ):
            result = self.psql("SELECT has_function_privilege('braidplane_executor', 'public." + capability + "', 'EXECUTE');")
            self.assertEqual(result.stdout.strip(), "t", capability)
        self.psql("SET ROLE braidplane_migrator; CREATE FUNCTION public.fixture_future_capability() RETURNS int LANGUAGE sql AS 'SELECT 1';")
        denied = self.psql("SET ROLE braidplane_executor; SELECT public.fixture_future_capability();", check=False)
        self.assertNotEqual(denied.returncode, 0)
        self.assertIn("permission denied", denied.stderr)
        self.psql("SET ROLE braidplane_runtime; SELECT public.fixture_future_capability();")
        for call in ("claim_next_execution_request()", "execution_request_metrics()", "connector_execution_admission_metrics()"):
            self.psql("SET ROLE braidplane_executor; SELECT * FROM " + call)
        self.psql("SET ROLE braidplane_executor; SELECT * FROM core_schema_migrations;")
        for sql in ("SELECT * FROM credentials", "SELECT * FROM user_sessions", "SELECT * FROM execution_requests",
                    "CREATE TABLE public.forbidden(id int)", "DELETE FROM core_schema_migrations",
                    "SELECT * FROM claim_execution_request(gen_random_uuid())"):
            with self.subTest(denied=sql):
                result = self.psql("SET ROLE braidplane_executor; " + sql, check=False)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("permission denied", result.stderr)
        self.psql("SET ROLE braidplane_runtime; SELECT * FROM execution_request_metrics();")
        self.psql("GRANT fixture_inherited TO braidplane_executor;")
        rejected = self.psql(execution, check=False)
        self.assertNotEqual(rejected.returncode, 0)
        self.assertIn("no memberships", rejected.stderr)


if __name__ == "__main__":
    unittest.main()
