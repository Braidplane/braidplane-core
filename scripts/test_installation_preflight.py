import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

from scripts.installation.preflight import check_tls


@unittest.skipUnless(shutil.which("openssl"), "OpenSSL required for certificate fixture")
class TLSPreflightTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.certs = self.root / "certificates"
        self.certs.mkdir()
        def openssl(*args):
            subprocess.run(["openssl", *args], cwd=self.certs, check=True, capture_output=True)
        openssl("req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
                "-nodes", "-days", "2", "-addext", "basicConstraints=critical,CA:true",
                "-addext", "keyUsage=critical,keyCertSign,cRLSign", "-subj", "/CN=Test CA", "-keyout", "ca.key", "-out", "ca.pem")
        for name, host in (("database", "database"), ("edge", "app.customer.test")):
            openssl("req", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
                    "-subj", "/CN=" + host, "-keyout", name + ".key", "-out", name + ".csr")
            (self.certs / "extensions.cnf").write_text("subjectAltName=DNS:" + host + "\nextendedKeyUsage=serverAuth\nsubjectKeyIdentifier=hash\nauthorityKeyIdentifier=keyid,issuer\nbasicConstraints=critical,CA:false\nkeyUsage=critical,digitalSignature\n")
            openssl("x509", "-req", "-in", name + ".csr", "-CA", "ca.pem", "-CAkey", "ca.key",
                    "-CAcreateserial", "-days", "2", "-extfile", "extensions.cnf", "-out", name + ".pem")
            (self.certs / (name + ".key")).chmod(0o600)

    def test_customer_chain_and_hostnames(self):
        check_tls(self.root, "app.customer.test")
        with self.assertRaisesRegex(ValueError, "TLS validation failed"):
            check_tls(self.root, "wrong.customer.test")
        (self.certs / "ca.pem").write_text("invalid certificate")
        with self.assertRaises(ValueError):
            check_tls(self.root, "app.customer.test")

    def test_private_key_mismatch_and_permissions(self):
        (self.certs / "edge.key").write_bytes((self.certs / "database.key").read_bytes())
        with self.assertRaisesRegex(ValueError, "do not match"):
            check_tls(self.root, "app.customer.test")
        (self.certs / "edge.key").chmod(0o644)
        with self.assertRaisesRegex(ValueError, "only to its owner"):
            check_tls(self.root, "app.customer.test")
        (self.certs / "edge.key").unlink()
        (self.certs / "edge.key").symlink_to(self.certs / "database.key")
        with self.assertRaisesRegex(ValueError, "regular file"):
            check_tls(self.root, "app.customer.test")


if __name__ == "__main__":
    unittest.main()
