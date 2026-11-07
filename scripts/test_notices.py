import json
import contextlib
import io
from pathlib import Path
import tempfile
import subprocess
import unittest
from unittest.mock import patch

from scripts import generate_notices


class NoticeTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "MPL_SOURCES.sha256").write_text("")
        for name in (
            "pkg/odcs/schema/LICENSE", "brand/icons/app/LICENSE",
            "frontend/assets/vendor/LICENSE-widget", "brand/icons/data-sources/licenses/DISCLAIMER",
        ):
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(f"{name} notice\n")
        packages = {}
        for name in ("tailwindcss", "daisyui"):
            path = self.root / "frontend/node_modules" / name
            path.mkdir(parents=True)
            (path / "package.json").write_text('{"version":"1.2.3"}')
            (path / "LICENSE").write_text(f"{name} license\n")
            packages[f"node_modules/{name}"] = {"version": "1.2.3"}
        (self.root / "frontend/package-lock.json").write_text(json.dumps({"packages": packages}))

    def test_combines_target_graphs_and_asset_notices(self):
        with patch.object(generate_notices, "ROOT", self.root), \
                patch.object(generate_notices, "go_json", return_value=[{"package": "fixture"}]) as graph, \
                patch.object(generate_notices, "generator_notices", return_value=b"Generator fixture notices\n"), \
                patch.object(generate_notices, "third_party_notices", return_value=b"Go fixture notices\n") as notices:
            result = generate_notices.generate()
        self.assertEqual(graph.call_count, 10)
        self.assertEqual(len(notices.call_args.kwargs["package_graph"]), 10)
        for call in graph.call_args_list:
            self.assertIn(call.args[1]["CGO_ENABLED"], ("0", "1"))
            self.assertEqual(call.args[1]["GOWORK"], "off")
            self.assertEqual(call.args[3], generate_notices.GO)
            if call.args[1]["GOOS"] == "windows":
                self.assertEqual(call.args[2], ("./cli/cmd/braidplane",))
                self.assertEqual(call.args[1]["CGO_ENABLED"], "0")
            else:
                self.assertEqual(call.args[2], generate_notices.PACKAGES)
        self.assertEqual(
            {(call.args[1]["GOOS"], call.args[1]["GOARCH"], call.args[1]["CGO_ENABLED"]) for call in graph.call_args_list},
            {(goos, arch, cgo) for goos, arch in generate_notices.TARGETS for cgo in ("0", "1")}
            | {("windows", arch, "0") for arch in ("amd64", "arm64")},
        )
        for text in (b"Go fixture notices", b"pkg/odcs/schema/LICENSE notice", b"tailwindcss license", b"daisyui license", b"DISCLAIMER notice", b"Generator fixture notices"):
            self.assertIn(text, result)
        self.assertNotIn(str(self.root).encode(), result)

    def test_refuses_css_input_version_mismatch(self):
        path = self.root / "frontend/node_modules/daisyui/package.json"
        path.write_text('{"version":"9.9.9"}')
        with patch.object(generate_notices, "ROOT", self.root), \
                patch.object(generate_notices, "go_json", return_value=[]), \
                patch.object(generate_notices, "third_party_notices", return_value=b""), \
                self.assertRaisesRegex(ValueError, "daisyui installation does not match lockfile"):
            generate_notices.generate()

    def test_check_refuses_stale_inventory_without_overwriting(self):
        output = self.root / "THIRD_PARTY_NOTICES.txt"
        output.write_bytes(b"old notices")
        errors = io.StringIO()
        with patch.object(generate_notices, "ROOT", self.root), \
                patch.object(generate_notices, "generate", return_value=b"new notices"), \
                patch("sys.argv", ["generate_notices", "--check"]), \
                contextlib.redirect_stderr(errors):
            self.assertEqual(generate_notices.main(), 1)
        self.assertEqual(output.read_bytes(), b"old notices")
        self.assertIn("is stale", errors.getvalue())

    def test_retains_pinned_generator_legal_texts(self):
        directory = self.root / "generator"
        directory.mkdir()
        (directory / "LICENSE").write_text("Generator legal terms\n")

        def download(root, env, command, *args):
            self.assertIn("-modfile=tools/go.mod", args)
            return json.dumps({"Path": args[-1], "Version": "v1.2.3", "Dir": str(directory)})

        with patch.object(generate_notices, "go_stdout", side_effect=download):
            output = generate_notices.generator_notices(self.root, {})
        self.assertEqual(output.count(b"Generator legal terms"), len(generate_notices.GENERATORS))
        self.assertIn(b"v1.2.3 (generator input)", output)
        self.assertNotIn(str(self.root).encode(), output)

    def test_generator_failure_preserves_go_diagnostics(self):
        result = subprocess.CompletedProcess([], 1, "go: exact synthetic diagnostic\n")
        errors = io.StringIO()
        with patch("cli.scripts.release.subprocess.run", return_value=result), \
                contextlib.redirect_stderr(errors), \
                self.assertRaises(subprocess.CalledProcessError):
            generate_notices.generator_notices(self.root, {})
        self.assertEqual(errors.getvalue(), result.stdout)


if __name__ == "__main__":
    unittest.main()
