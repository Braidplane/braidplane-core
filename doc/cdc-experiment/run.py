"""Run issue #248's isolated database probes. Requires Python 3 and local Docker."""

import argparse
import select
import subprocess
import sys
import time
import uuid
from contextlib import contextmanager
from pathlib import Path


IMAGES = {
    "postgres": "postgres@sha256:edb0c08dc33befd0262ab3b5cf1c4f3f8cfd8b09e88c05b45adee29d6761ddb6",
    "mysql": "mysql@sha256:135bc87cce147c3d28cecb9ad270b814cb52805af7ddeea83bfcaf157d05a6b2",
    "mongo": "mongo@sha256:e0ce8c35124d4a9f9785532d1f268f39e9728ffa1cb38f46fa482436424c4bd3",
}
PG = ["psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-h127.0.0.1", "-dcdc"]
MYSQL = ["mysql", "--batch", "--skip-column-names", "--unbuffered", "-h127.0.0.1"]
MONGO_ADMIN = ["mongosh", "--quiet", "--norc", "mongodb://127.0.0.1:27017/admin?directConnection=true",
               "-u", "root", "-p", "cdc-fixture"]


def command(args, *, input=None, check=True, timeout=90):
    return subprocess.run(args, input=input, text=True, capture_output=True, check=check, timeout=timeout)


def show(label, value):
    print(f"{label} {value}", flush=True)


@contextmanager
def fixture(engine, env, *options, entrypoint=None):
    name = f"core-248-{engine}-{uuid.uuid4().hex[:8]}"
    image = IMAGES[engine]
    docker_env = [arg for item in env for arg in ("-e", item)]
    entrypoint_option = ["--entrypoint", entrypoint] if entrypoint else []
    if command(["docker", "image", "inspect", image], check=False).returncode:
        command(["docker", "pull", image], timeout=600)
    docker_id = None
    try:
        docker_id = command(["docker", "run", "-d", "--pull=never", "--network", "none",
                             "--name", name, *docker_env, *entrypoint_option, image, *options]).stdout.strip()
        show("fixture", f"{name} {image} {docker_id[:12]} (no network or host ports)")
        yield ["docker", "exec", "-i", docker_id]
    finally:
        command(["docker", "rm", "-fv", docker_id or name], check=docker_id is not None)


def wait_ready(args, statement):
    for _ in range(60):
        result = command(args, input=statement, check=False)
        if result.returncode == 0:
            return
        if "container" in result.stderr and "is not running" in result.stderr:
            logs = command(["docker", "logs", "--tail", "12", args[3]])
            raise RuntimeError(f"fixture exited during setup:\n{logs.stdout}\n{logs.stderr}")
        time.sleep(1)
    raise RuntimeError(f"fixture did not become ready: {result.stderr.strip()}")


@contextmanager
def mysql_session(args):
    with subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True) as session:
        try:
            yield session
        finally:
            if session.poll() is None:
                session.kill()


def pos_lsn(data):
    value = int.from_bytes(data, "big")
    return f"{value >> 32:X}/{value & 0xffffffff:X}"


def postgres():
    with fixture("postgres", ["POSTGRES_PASSWORD=cdc-fixture", "POSTGRES_DB=cdc"],
                 "-c", "wal_level=logical", "-c", "max_slot_wal_keep_size=0") as run:
        admin = run + ["env", "PGPASSWORD=cdc-fixture", *PG, "-Upostgres"]
        reader = run + ["env", "PGPASSWORD=cdc-reader", *PG, "-Ucapture"]
        wait_ready(admin, "SELECT 1;")
        show("version", command(admin, input="SHOW server_version;").stdout.strip())
        command(admin, input="""
            CREATE ROLE capture LOGIN REPLICATION PASSWORD 'cdc-reader';
            REVOKE CONNECT, TEMPORARY ON DATABASE cdc FROM PUBLIC;
            GRANT CONNECT ON DATABASE cdc TO capture;
            CREATE SCHEMA sample;
            CREATE TABLE sample.items (id int PRIMARY KEY, value text NOT NULL);
            INSERT INTO sample.items VALUES (0, 'seed');
            CREATE PUBLICATION capture_pub FOR TABLE sample.items
              WITH (publish = 'insert, update, delete');
            GRANT USAGE ON SCHEMA sample TO capture;
            GRANT SELECT ON sample.items TO capture;
        """)
        denied = command(reader, input="DELETE FROM sample.items;", check=False)
        assert denied.returncode and "permission denied" in denied.stderr, denied
        show("capture write denied", denied.stderr.strip())

        # The second psql commits after slot creation and before the snapshot read.
        snapshot = command(reader + ["-d", "dbname=cdc replication=database"], input=r"""
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
CREATE_REPLICATION_SLOT capture_slot LOGICAL pgoutput (SNAPSHOT 'use');
\! PGPASSWORD=cdc-fixture psql -Xq -v ON_ERROR_STOP=1 -h127.0.0.1 -Upostgres -dcdc -c "INSERT INTO sample.items VALUES (1, 'inserted')"
SELECT id, value FROM sample.items ORDER BY id;
COMMIT;
""").stdout
        show("slot creation and snapshot", snapshot.strip())
        assert snapshot.splitlines()[2] == "0|seed", snapshot
        assert "1|inserted" not in snapshot, snapshot

        def peek():
            rows = command(reader, input="""
                SELECT lsn, xid, encode(data, 'hex')
                FROM pg_logical_slot_peek_binary_changes(
                    'capture_slot', NULL, NULL, 'proto_version', '1',
                    'publication_names', 'capture_pub');
            """).stdout.splitlines()
            events = []
            for row in rows:
                lsn, xid, hex_data = row.split("|")
                data = bytes.fromhex(hex_data)
                kind = chr(data[0])
                if kind == "C":
                    events.append((kind, lsn, xid, pos_lsn(data[2:10]), pos_lsn(data[10:18])))
                else:
                    events.append((kind, lsn, xid, hex_data))
            return events

        first = peek()
        show("first read (unacknowledged)", first)
        assert [event[0] for event in first] == ["B", "R", "I", "C"], first
        assert bytes.fromhex(first[2][-1]).endswith(b"t\x00\x00\x00\x08inserted"), first
        command(admin, input="UPDATE sample.items SET value = 'updated' WHERE id = 1; "
                             "DELETE FROM sample.items WHERE id = 1;")
        replay = peek()
        show("fresh reader, old checkpoint", replay)
        assert [event[0] for event in replay if event[0] not in "BR"] == list("ICUCDC"), replay
        assert [event for event in replay if event[0] in "IC"][:2] == first[2:], replay
        updated, deleted = [bytes.fromhex(event[-1]) for event in replay if event[0] in "UD"]
        assert updated.endswith(b"t\x00\x00\x00\x07updated") and deleted[5:6] == b"K"
        commits = [event for event in replay if event[0] == "C"]
        end_insert = commits[0][-1]
        advanced = command(reader, input=f"SELECT end_lsn FROM pg_replication_slot_advance("
                           f"'capture_slot', '{end_insert}');").stdout.strip()
        assert advanced == end_insert, advanced
        remaining = peek()
        show("fresh reader after acknowledging insert", remaining)
        assert [event[0] for event in remaining if event[0] not in "BR"] == list("UCDC"), remaining
        assert [event for event in remaining if event[0] == "C"] == commits[1:], remaining

        for _ in range(3):
            command(admin, input="CREATE TABLE IF NOT EXISTS churn (n int); INSERT INTO churn VALUES (1);"
                                 "\nSELECT pg_switch_wal();\nCHECKPOINT;")
        state = command(admin, input="SELECT wal_status, invalidation_reason FROM pg_replication_slots "
                        "WHERE slot_name = 'capture_slot';").stdout.strip()
        expired = command(reader, input="SELECT * FROM pg_logical_slot_peek_binary_changes("
                          "'capture_slot', NULL, NULL, 'proto_version', '1', "
                          "'publication_names', 'capture_pub');", check=False)
        show("expired slot", f"{state}\n{expired.stderr.strip()}")
        assert state == "lost|wal_removed" and expired.returncode, (state, expired)


def mysql():
    with fixture("mysql", ["MYSQL_ROOT_PASSWORD=cdc-fixture"], "--server-id=248",
                 "--log-bin=cdc-bin", "--binlog-format=ROW", "--binlog-row-image=FULL") as run:
        admin = run + ["env", "MYSQL_PWD=cdc-fixture", *MYSQL, "-uroot"]
        reader = run + ["env", "MYSQL_PWD=cdc-reader", *MYSQL, "-ucapture"]
        wait_ready(admin, "SELECT 1;")
        show("version, identity, log settings", command(admin, input="""
            SELECT VERSION(), @@server_uuid, @@binlog_format, @@binlog_row_image, @@binlog_order_commits;
        """).stdout.strip())
        command(admin, input="""
            CREATE DATABASE sample;
            CREATE TABLE sample.items (id int PRIMARY KEY, value varchar(32) NOT NULL) ENGINE=InnoDB;
            INSERT INTO sample.items VALUES (0, 'seed');
            CREATE USER capture@'127.0.0.1' IDENTIFIED BY 'cdc-reader';
            GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO capture@'127.0.0.1';
            GRANT SELECT ON sample.items TO capture@'127.0.0.1';
        """)
        denied = command(reader, input="DELETE FROM sample.items;", check=False)
        assert denied.returncode and "command denied" in denied.stderr, denied
        show("capture write denied", denied.stderr.strip())

        # Freeze commits while the reader establishes its InnoDB snapshot and records the log position.
        with mysql_session(admin) as lock:
            lock.stdin.write("FLUSH TABLES WITH READ LOCK; SELECT 'locked';\n")
            lock.stdin.flush()
            assert select.select([lock.stdout], [], [], 15)[0], "Timed out acquiring snapshot lock"
            assert lock.stdout.readline().strip() == "locked"
            with mysql_session(reader) as snapshot:
                snapshot.stdin.write("SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ;"
                                     "START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY;"
                                     "SHOW BINARY LOG STATUS;\n")
                snapshot.stdin.flush()
                assert select.select([snapshot.stdout], [], [], 15)[0], "Timed out opening snapshot"
                filename, start, *_ = snapshot.stdout.readline().split("\t")
                lock.communicate("UNLOCK TABLES;\n", timeout=15)
                assert lock.returncode == 0
                command(admin, input="INSERT INTO sample.items VALUES (1, 'inserted');")
                data, stderr = snapshot.communicate("SELECT * FROM sample.items ORDER BY id; COMMIT;\n", timeout=15)
                assert snapshot.returncode == 0 and data.strip() == "0\tseed", (data, stderr)
        show("snapshot", f"{filename}:{start} {data.strip()}")

        def log_status():
            current_file, end, *_ = command(reader, input="SHOW BINARY LOG STATUS;").stdout.split("\t")
            assert current_file == filename
            return end

        def read_log(begin):
            return command(reader, input=f"SHOW BINLOG EVENTS IN '{filename}' FROM {begin};",
                           check=False)

        def verify(label, begin, end, operations):
            result = read_log(begin)
            assert result.returncode == 0, result.stderr
            rows = [line.split("\t") for line in result.stdout.splitlines()]
            actual = [row[2] for row in rows if row[2].endswith("_rows")]
            commits = [row[4] for row in rows if row[2] == "Xid"]
            assert actual == operations and len(commits) == len(operations), rows
            assert rows[0][1] == begin and rows[-1][4] == end, rows
            show(label, f"{filename}:{begin}..{end}\n" + result.stdout.strip())
            return commits

        insert_end = log_status()
        assert verify("first read (unacknowledged)", start, insert_end, ["Write_rows"]) == [insert_end]
        command(admin, input="UPDATE sample.items SET value='updated' WHERE id=1; DELETE FROM sample.items WHERE id=1;")
        end = log_status()
        replay = verify("fresh reader, old checkpoint", start, end, ["Write_rows", "Update_rows", "Delete_rows"])
        assert replay[0] == insert_end and replay[-1] == end
        assert verify("fresh reader after acknowledging insert", insert_end, end,
                      ["Update_rows", "Delete_rows"]) == replay[1:]

        command(admin, input="FLUSH BINARY LOGS;")
        rotated = command(reader, input="SHOW BINARY LOG STATUS;").stdout.split("\t")[0]
        assert filename != rotated
        command(admin, input=f"PURGE BINARY LOGS TO '{rotated}';")
        expired = read_log(start)
        show("purged binlog", expired.stderr.strip())
        assert expired.returncode and "Could not find target log" in expired.stderr, expired


def mongo():
    with fixture("mongo", ["MONGO_INITDB_ROOT_USERNAME=root", "MONGO_INITDB_ROOT_PASSWORD=cdc-fixture"],
                 "-ec", """
                 umask 077
                 head -c 512 /dev/urandom | base64 > /tmp/cdc-key
                 chown mongodb:mongodb /tmp/cdc-key
                 exec docker-entrypoint.sh mongod --replSet cdc248 --oplogSize 16 \
                     --syncdelay 1 --keyFile /tmp/cdc-key
                 """, entrypoint="bash") as run:
        admin = run + MONGO_ADMIN
        wait_ready(admin + ["--eval", "assert(db.adminCommand({getCmdLineOpts: 1})"
                            ".parsed.replication?.replSet === 'cdc248')"], "")
        result = command(admin + ["--eval", "const result = rs.initiate("
                          "{_id:'cdc248',members:[{_id:0,host:'localhost:27017'}]}); "
                          "assert(result.ok === 1); printjson(result)"])
        show("replica set initialization", result.stdout.strip())
        wait_ready(admin + ["--eval", "assert(db.hello().isWritablePrimary)"], "")
        script = Path(__file__).with_name("mongo.js").read_text()
        command(run + ["sh", "-c", "cat > /tmp/cdc-probe.js"], input=script)
        result = command(admin + ["--eval", "try { load('/tmp/cdc-probe.js'); } "
                          "catch (error) { console.error(error.stack); quit(1); }"], check=False)
        print(result.stdout, end="", flush=True)
        if result.returncode:
            raise RuntimeError(result.stderr)


if __name__ == "__main__":
    probes = {"postgres": postgres, "mysql": mysql, "mongo": mongo}
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("engine", choices=[*probes, "all"])
    engine = parser.parse_args().engine
    for probe in probes.values() if engine == "all" else [probes[engine]]:
        try:
            probe()
        except subprocess.CalledProcessError as error:
            print(error.stderr, file=sys.stderr)
            raise
        show("PASS", probe.__name__)
