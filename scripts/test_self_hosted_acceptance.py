import json
import importlib.util
import io
from pathlib import Path
import ssl
import tempfile
import unittest
from unittest.mock import Mock, patch
from urllib.error import HTTPError

from scripts.self_hosted_acceptance.prepare import prepare
from scripts.self_hosted_acceptance.check_connect import destinations


class SelfHostedFixturePreparationTests(unittest.TestCase):
    def test_customer_certificates_secrets_and_overwrite_protection(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary) / "fixture"
            prepare(target)
            bootstrap = (target / "compose.yml").read_text().split("  bootstrap:\n", 1)[1].split("  probe:\n", 1)[0]
            self.assertIn("entrypoint: [/braidplane-admin]", bootstrap)
            self.assertIn("command: [bootstrap,", bootstrap)
            context = ssl.create_default_context(cafile=str(target / "ca.pem"))
            self.assertEqual(ssl.CERT_REQUIRED, context.verify_mode)
            certificate = ssl._ssl._test_decode_cert(str(target / "server.pem"))
            self.assertIn(("DNS", "app.customer.test"), certificate["subjectAltName"])
            self.assertIn(("DNS", "database.customer.test"), certificate["subjectAltName"])
            for name in ("ca.key", "server.key", "runtime.env", "migrator.env", "database.env", "passwords.json", "bootstrap-password", "init.sql", "cli.env"):
                self.assertEqual(0o600, (target / name).stat().st_mode & 0o777, name)
            runtime = dict(line.split("=", 1) for line in (target / "runtime.env").read_text().splitlines())
            migrator = dict(line.split("=", 1) for line in (target / "migrator.env").read_text().splitlines())
            self.assertNotEqual(runtime["DB_PASSWORD"], migrator["DB_PASSWORD"])
            self.assertNotEqual(runtime["ENCRYPTION_KEY_HEX"], runtime["EXECUTION_REQUEST_KEY_HEX"])
            before = (target / "ca.key").read_bytes()
            with self.assertRaises(FileExistsError):
                prepare(target)
            self.assertEqual(before, (target / "ca.key").read_bytes())
            passwords = json.loads((target / "passwords.json").read_text())
            self.assertNotEqual(passwords["initial"], passwords["replacement"])


class ConnectTraceTests(unittest.TestCase):
    def test_internet_destinations_and_unknown_formats(self):
        trace = '\n'.join([
            'connect(3, {sa_family=AF_INET, sin_port=htons(5432), sin_addr=inet_addr("10.20.0.8")}, 16) = 0',
            'connect(4, {sa_family=AF_INET6, sin6_port=htons(5432), inet_pton(AF_INET6, "::ffff:10.20.0.8", &sin6_addr)}, 28) = 0',
            'connect(5, {sa_family=AF_UNIX, sun_path="/local/socket"}, 16) = 0',
        ])
        self.assertEqual({('10.20.0.8', 5432): 2}, destinations(trace))
        with self.assertRaises(AssertionError):
            destinations('connect(3, {sa_family=AF_INET, unknown_destination}, 16) = 0')


class FrontendProbeTests(unittest.TestCase):
    def setUp(self):
        path = Path(__file__).parent / 'self_hosted_acceptance/frontend_security.py'
        spec = importlib.util.spec_from_file_location('frontend_probe', path)
        probe = importlib.util.module_from_spec(spec)
        context = ssl.create_default_context()
        with patch('ssl.create_default_context', return_value=context):
            spec.loader.exec_module(probe)
        self.probe = probe

    def test_readiness_waits_for_api_and_frontend_and_is_bounded(self):
        probe = self.probe
        probe.client = Mock()
        ready = Mock(status=200)
        ready.__enter__ = Mock(return_value=ready)
        ready.__exit__ = Mock(return_value=False)
        probe.client.open.side_effect = [HTTPError(probe.base, 503, 'starting', {}, None), ready, ready]
        with patch.object(probe.time, 'sleep') as sleep:
            probe.wait_ready()
            sleep.assert_called_once_with(1)
        self.assertEqual(['http://api:3001/readyz', 'http://api:3001/readyz', probe.base + '/auth/login'],
                         [call.args[0] for call in probe.client.open.call_args_list])
        probe.client.open.reset_mock()
        probe.client.open.side_effect = OSError('synthetic unavailable endpoint')
        with patch.object(probe.time, 'sleep') as sleep:
            with self.assertRaises(OSError):
                probe.wait_ready()
            self.assertEqual(29, sleep.call_count)
        self.assertEqual(30, probe.client.open.call_count)

    def test_auth_throttle_retries_one_default_window_then_fails(self):
        probe = self.probe
        def response(status):
            return HTTPError(probe.base, status, 'synthetic', {}, io.BytesIO(b'body'))
        probe.client = Mock()
        probe.client.open.side_effect = [response(429), response(200)]
        with patch.object(probe.time, 'sleep') as sleep:
            self.assertEqual('body', probe.request('/auth/login')[0])
            sleep.assert_called_once_with(60)
        probe.client.open.side_effect = [response(429), response(429)]
        with patch.object(probe.time, 'sleep') as sleep:
            with self.assertRaises(AssertionError):
                probe.request('/auth/login')
            sleep.assert_called_once_with(60)
