#!/usr/bin/env python3
"""Exercise lint boundary policies with compile-valid disposable modules."""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
TOOL_ROOT = ROOT
PRIVATE = "github.com/Braidplane/braidplane-core/"
CLI = PRIVATE

# Filename, imports, expected rule names. Empty rules prove intentional edges.
CASES = [
    ("pkg/connector/filestream/allowed.go", [PRIVATE + "pkg/payload/file", PRIVATE + "pkg/slug", PRIVATE + "pkg/uuid", PRIVATE + "pkg/xerror"], set()),
    ("pkg/connector/filestream/probe.go", [PRIVATE + "postgres/fixture"], {"neutral_internal_lower_layers"}),
    ("frontend/native_transfer.go", [PRIVATE + "pkg/connector/filestream"], {"frontend_boundary"}),
    ("pkg/contractplan/allowed.go", [PRIVATE + "pkg/domain/model", PRIVATE + "pkg/odcs", CLI + "pkg/pipeline/payload"], set()),
    ("pkg/odcs/allowed.go", [PRIVATE + "pkg/domain/model", CLI + "pkg/pipeline/payload"], set()),
    ("pkg/contractplan/git.go", [PRIVATE + "pkg/gitprovider"], {"contract_compilation_boundary"}),
    ("pkg/odcs/nested/git.go", [PRIVATE + "pkg/repostore"], {"contract_compilation_boundary"}),
    ("pkg/contractplan/credentials.go", [PRIVATE + "pkg/crypto"], {"contract_compilation_boundary"}),
    ("pkg/odcs/execution.go", [PRIVATE + "pkg/queue", PRIVATE + "pkg/pipeline/run", PRIVATE + "pkg/executiondelivery"], {"contract_compilation_boundary"}),
    ("pkg/contractplan/catalog.go", [PRIVATE + "postgres/fixture"], {"neutral_internal_lower_layers"}),
    ("pkg/odcs/catalog.go", [PRIVATE + "service/fixture"], {"neutral_internal_lower_layers"}),
    ("pkg/contractplan/probe_test.go", [PRIVATE + "pkg/gitprovider"], set()),
    ("frontend/sink_contract.go", [PRIVATE + "pkg/sinkbinding/fixture"], {"frontend_boundary"}),
    ("frontend/workspace_contract.go", [CLI + "pkg/connector/googleworkspace/fixture"], {"frontend_boundary"}),
    ("frontend/cmd/wif-reconciler/probe.go", [PRIVATE + "pkg/spireregistration"], {"spire_registration_boundary"}),
    ("pkg/queue/exact.go", [PRIVATE + "postgres/executionrequests/fixture"], {"queue_persistence_boundary"}),
    ("pkg/proofevidence/exact.go", [PRIVATE + "pkg/domain/model/fixture"], {"proof_evidence_boundary"}),
    ("pkg/googleworkspace/probe.go", [PRIVATE + "pkg/connector/result"], {"connector_provider_client_boundary"}),
    ("pkg/gitlab/oauth/probe.go", [CLI + "pkg/pipeline/spec"], {"connector_provider_client_boundary"}),
    ("pkg/snowflakewif/probe.go", [CLI + "pkg/pipeline/spec"], {"connector_provider_client_boundary"}),
    ("main.go", [PRIVATE + "frontend/fixture"], {"backend_composition_boundary"}),
    ("frontend/probe.go", [PRIVATE + "postgres/fixture"], {"frontend_boundary"}),
    ("frontend/nested/probe.go", [PRIVATE + "service/fixture"], {"frontend_boundary"}),
    ("frontend/allowed.go", [PRIVATE + "api-schema/gen/client", PRIVATE + "pkg/sinkbinding", CLI + "pkg/connector/googleworkspace"], set()),
    ("frontend/denied.go", [CLI + "pkg/connector/catalog"], {"frontend_boundary"}),
    ("cmd/frontend/probe.go", [PRIVATE + "pkg/googleworkspace"], {"frontend_boundary"}),
    ("config/frontend/probe.go", [PRIVATE + "postgres/fixture"], {"frontend_config_boundary"}),
    ("config/frontend/allowed.go", [PRIVATE + "pkg/dbpool", PRIVATE + "pkg/httpsecurity"], set()),
    ("config/backend/probe.go", [PRIVATE + "frontend/fixture"], {"backend_config_boundary"}),
    ("backend/frontend.go", [PRIVATE + "frontend/fixture"], {"backend_composition_boundary"}),
    ("backend/allowed.go", [PRIVATE + "service/fixture"], set()),
    ("service/probe.go", [PRIVATE + "api-schema/gen/server"], {"service_boundary"}),
    ("service/nested/probe.go", [PRIVATE + "transport/http/fixture"], {"service_boundary"}),
    ("service/nested/probe_test.go", [PRIVATE + "transport/http/fixture"], set()),
    ("service/allowed.go", [PRIVATE + "postgres/fixture"], set()),
    ("service/executionbroker/probe.go", [PRIVATE + "service/worker/fixture"], {"execution_broker_boundary"}),
    ("service/executionbroker/allowed.go", [PRIVATE + "pkg/executiondelivery"], set()),
    ("postgres/probe.go", [PRIVATE + "service/fixture"], {"postgres_boundary"}),
    ("postgres/nested/probe.go", [PRIVATE + "transport/http/fixture"], {"postgres_boundary"}),
    ("postgres/allowed.go", [PRIVATE + "pkg/domain/model"], set()),
    ("transport/http/probe.go", [PRIVATE + "postgres/fixture"], {"transport_http_boundary"}),
    ("transport/http/nested/probe.go", [PRIVATE + "api-schema/gen/client"], {"transport_http_boundary"}),
    ("transport/http/probe_test.go", [PRIVATE + "postgres/fixture"], set()),
    ("transport/http/nested/probe_test.go", [PRIVATE + "postgres/fixture"], set()),
    ("transport/http/allowed.go", [PRIVATE + "service/fixture"], set()),
    ("pkg/clock/probe.go", [PRIVATE + "service/fixture"], {"neutral_internal_lower_layers"}),
    ("pkg/clock/neutral/probe.go", [PRIVATE + "postgres/fixture"], {"neutral_internal_lower_layers"}),
    ("pkg/clock/neutral/transport.go", [PRIVATE + "transport/http/fixture"], {"internal_boundary"}),
    ("pkg/clock/neutral/probe_test.go", [PRIVATE + "service/fixture", PRIVATE + "transport/http/fixture"], set()),
    ("pkg/queue/allowed.go", [PRIVATE + "postgres", PRIVATE + "postgres/executionrequests"], set()),
    ("pkg/queue/probe.go", [PRIVATE + "postgres/fixture"], {"queue_persistence_boundary"}),
    ("pkg/queue/service.go", [PRIVATE + "service/fixture"], {"queue_persistence_boundary"}),
    ("pkg/eventtest/allowed.go", [PRIVATE + "service/fixture", PRIVATE + "postgres/fixture"], set()),
    ("pkg/fake/allowed.go", [PRIVATE + "transport/http/fixture", PRIVATE + "service/fixture"], set()),
    ("pkg/testutil/nested/allowed.go", [PRIVATE + "postgres/fixture", PRIVATE + "service/fixture"], set()),
    ("service/test_visibility.go", [PRIVATE + "pkg/eventtest"], {"test_composition_visibility"}),
    ("service/test_visibility_test.go", [PRIVATE + "pkg/eventtest"], set()),
    ("pkg/pipeline/run/probe.go", [PRIVATE + "service/fixture"], {"pipeline_internal_boundary", "neutral_internal_lower_layers"}),
    ("pkg/proofevidence/probe.go", [PRIVATE + "pkg/ptr"], {"proof_evidence_boundary"}),
    ("pkg/proofevidence/allowed.go", [PRIVATE + "pkg/domain/model", CLI + "pkg/slug"], set()),
    ("pkg/proofevidence/probe_test.go", [PRIVATE + "pkg/ptr"], set()),
    ("service/proofconfigsvc/probe.go", [PRIVATE + "pkg/ptr"], {"proof_configuration_boundary"}),
    ("service/proofconfigsvc/allowed.go", [PRIVATE + "postgres/proofconfig"], set()),
    ("service/proofconfigsvc/probe_test.go", [PRIVATE + "pkg/ptr"], set()),
    ("service/datasourcessvc/connector/probe.go", [PRIVATE + "postgres/fixture"], {"connector_boundary"}),
    ("service/datasourcessvc/connector/sql/postgres/allowed.go", [PRIVATE + "service/datasourcessvc/connector/param"], set()),
    ("service/datasourcessvc/connector/example/client/probe.go", [PRIVATE + "pkg/connector/result"], {"connector_provider_client_boundary"}),
    ("pkg/gitprovider/probe.go", [PRIVATE + "pkg/connector/result"], {"connector_provider_client_boundary"}),
    ("pkg/hubspot/crm/probe.go", [CLI + "pkg/pipeline/spec"], {"connector_provider_client_boundary"}),
    ("pkg/googleoauth/probe.go", [CLI + "pkg/pipeline/spec"], {"connector_provider_client_boundary"}),
    ("service/datasourcessvc/connector/bigquery/probe.go", [PRIVATE + "pkg/googleoauth"], {"bigquery_execution_auth_boundary"}),
    ("service/privileged.go", [PRIVATE + "pkg/spireregistration"], {"spire_registration_boundary"}),
    ("cmd/wif-reconciler/probe.go", [PRIVATE + "pkg/spireregistration"], {"spire_registration_boundary"}),
    ("cli/cmd/probe.go", [PRIVATE + "service/fixture"], set()),
    ("cli/cmd/allowed.go", [CLI + "cli/apiclient"], set()),
    ("pkg/pipeline/example/probe.go", [CLI + "cli/apiclient"], {"cli_boundary"}),
    ("pkg/pipeline/example/allowed.go", [CLI + "pkg/model"], set()),
]


OPTION_CASES = {
    "service/options/admission.go": ("", "WithAdmissionSvc"),
    "service/options/local.go": ("", "WithRequiredRepository"),
    "service/options/local_test.go": ("", "WithRequiredRepository"),
    "service/options/unused.go": ("", "WithUnusedClient"),
    "main.go": ("pipelinesvc", "WithTransformExecutor"),
    "backend/runtime.go": ("pipelinesvc", "WithTransformExecutor"),
    "other.go": ("pipelinesvc", "WithTransformExecutor"),
    "service/pipelinesvc/service.go": ("pipelineruntime", "WithTransformExecutor"),
    "service/pipelinesvc/other.go": ("pipelineruntime", "WithTransformExecutor"),
    "service/pipelinesvc/required.go": ("pipelineruntime", "WithRequiredExecutor"),
    "scalar.go": ("renamed", "WithTimeout"),
    "limits.go": ("renamed", "WithTransformLimits"),
    "nonservice.go": ("external", "WithRequiredClient"),
    "dot.go": (".", "WithClient"),
    "reference.go": ("renamed", "WithRequiredClient"),
}

class PackageBoundaryTest(unittest.TestCase):
    maxDiff = None

    def test_service_dependency_options_are_allowed(self):
        suffixes = ("Repository", "Client", "Executor", "Factory", "Manager", "Pool")
        functions = [f"WithRequired{suffix}" for suffix in suffixes]
        functions += ["WithAdmissionSvc", "WithClient", "WithTransformExecutor", "WithTimeout", "WithTransformLimits"]
        cases = dict(OPTION_CASES)
        cases.update({f"probe_{suffix.lower()}.go": ("renamed", f"WithRequired{suffix}")
                      for suffix in suffixes})
        with tempfile.TemporaryDirectory(prefix="instance-options-") as temporary:
            root = Path(temporary).resolve()
            (root / "go.mod").write_text(f"module {PRIVATE[:-1]}\n\ngo 1.26.0\n")
            shutil.copyfile(ROOT / ".golangci.yml", root / ".golangci.yml")
            for package in ("service/options", "service/pipelinesvc", "service/pipelineruntime", "external"):
                path = root / package / "options.go"
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(f"package {Path(package).name}\ntype Option func()\n" +
                                "".join(f"func {name}(dependency any) Option {{ return func() {{}} }}\n"
                                        for name in functions))
            for filename, (alias, name) in cases.items():
                package = Path(filename).parent.name if "/" in filename else "fixture"
                imported = ""
                if alias:
                    target = {"pipelinesvc": "service/pipelinesvc", "pipelineruntime": "service/pipelineruntime",
                              "external": "external"}.get(alias, "service/options")
                    imported = f'import {alias} "{PRIVATE}{target}"\n'
                expression = f"{alias}.{name}" if alias and alias != "." else name
                body = f"var _ = {expression}(nil)\n"
                if filename == "reference.go":
                    body = f"var _ = {expression}\n"
                elif filename.endswith("unused.go"):
                    body = f"func {name}(dependency any) Option {{ return func() {{}} }}\n"
                (root / filename).parent.mkdir(parents=True, exist_ok=True)
                (root / filename).write_text(f"package {package}\n{imported}{body}")
            # Keep an unrelated forbidden call to prove forbidigo still runs.
            (root / "forbidden.go").write_text('package fixture\nimport "fmt"\nvar _ = fmt.Errorf("synthetic error")\n')
            environment = dict(os.environ, GOWORK="off")
            go = ["bash", str(TOOL_ROOT / "scripts/go-toolchain.sh")]
            compiled = subprocess.run(go + ["test", "./..."], cwd=root, env=environment, capture_output=True, text=True)
            self.assertEqual(compiled.returncode, 0, compiled.stdout + compiled.stderr)
            linted = subprocess.run(go + ["tool", f"-modfile={TOOL_ROOT / 'tools/go.mod'}", "golangci-lint", "run",
                f"--config={root / '.golangci.yml'}", "--path-mode=abs", "--enable-only=forbidigo", "--allow-parallel-runners",
                "--max-issues-per-linter=0", "--max-same-issues=0", "--show-stats=false",
                "--output.text.path=/dev/null", "--output.json.path=stdout", "./..."],
                cwd=root, env=environment, capture_output=True, text=True)
            self.assertEqual(linted.returncode, 1, linted.stdout + linted.stderr)
            issues = json.loads(linted.stdout).get("Issues") or []
            self.assertEqual(len(issues), 1, issues)
            self.assertEqual(issues[0]["FromLinter"], "forbidigo")
            self.assertEqual(Path(issues[0]["Pos"]["Filename"]).relative_to(root).as_posix(), "forbidden.go")
            self.assertIn("use xerror constructors", issues[0]["Text"])

    def test_real_linter_policy(self):
        with tempfile.TemporaryDirectory(prefix="package-boundaries-") as temporary:
            root = Path(temporary).resolve()
            (root / "go.mod").write_text(f"module {PRIVATE[:-1]}\n\ngo 1.26.0\n")
            shutil.copyfile(ROOT / ".golangci.yml", root / ".golangci.yml")
            for _, imports, _ in CASES:
                for imported in imports:
                    destination = root / imported.removeprefix(PRIVATE)
                    destination.mkdir(parents=True, exist_ok=True)
                    (destination / "stub.go").write_text("package fixture\n")
            for filename, imports, _ in CASES:
                path = root / filename
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("package fixture\nimport (\n" + "".join(f'_ "{name}"\n' for name in imports) + ")\n")
            environment = dict(os.environ, GOWORK="off")
            go = ["bash", str(TOOL_ROOT / "scripts/go-toolchain.sh")]
            for module in (root,):
                # Prove failures below are policy rejections, never broken Go imports.
                compiled = subprocess.run(go + ["test", "./..."], cwd=module, env=environment, capture_output=True, text=True)
                self.assertEqual(compiled.returncode, 0, compiled.stdout + compiled.stderr)
                linted = subprocess.run(go + ["tool", f"-modfile={TOOL_ROOT / 'tools/go.mod'}", "golangci-lint", "run", f"--config={root / '.golangci.yml'}", "--path-mode=abs", "--enable-only=depguard", "--allow-parallel-runners", "--max-issues-per-linter=0", "--max-same-issues=0", "--uniq-by-line=false", "--show-stats=false", "--output.text.path=/dev/null", "--output.json.path=stdout", "./..."], cwd=module, env=environment, capture_output=True, text=True)
                self.assertEqual(linted.returncode, 1, linted.stdout + linted.stderr)
                issues = json.loads(linted.stdout).get("Issues") or []
                observed = {}
                for issue in issues:
                    self.assertEqual(issue["FromLinter"], "depguard", issue)
                    name = Path(issue["Pos"]["Filename"])
                    self.assertTrue(name.is_absolute(), issue)
                    filename = name.relative_to(root).as_posix()
                    rule = re.search(r"from list '([^']+)'", issue["Text"])
                    self.assertIsNotNone(rule, issue)
                    observed.setdefault(filename, set()).add(rule.group(1))
                expected = {filename: rules for filename, _, rules in CASES if rules}
                self.assertEqual(observed, expected)


if __name__ == "__main__":
    unittest.main()
