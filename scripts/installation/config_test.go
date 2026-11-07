package installation_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	backend "github.com/Braidplane/braidplane-core/config/backend"
	frontend "github.com/Braidplane/braidplane-core/config/frontend"
)

// TestPreparedConfiguration checks the Python-generated bundle with the real loaders.
func TestPreparedConfiguration(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	command := exec.CommandContext(t.Context(), "python3", "-c", `
import json, sys
from pathlib import Path
from scripts.installation.prepare import prepare
from scripts.test_installation_release import manifest
root = Path(sys.argv[1])
(root / 'release.json').write_text(json.dumps(manifest()))
(root / 'tls').mkdir()
for name in ('ca.pem', 'database.pem', 'database.key', 'edge.pem', 'edge.key'):
    (root / 'tls' / name).write_text('synthetic fixture')
prepare(root / 'bundle', root / 'release.json', 'app.customer.test', '192.168.1.20',
        'mail.customer.test:587', 'recovery@customer.test', root / 'tls')
`, directory)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("prepare fixture: %v: %s", err, output)
	}
	bundle := filepath.Join(directory, "bundle")
	for _, role := range []string{"runtime", "executor"} {
		t.Run(role, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(bundle, role+".env"))
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
				key, value, found := strings.Cut(line, "=")
				if !found {
					t.Fatal("invalid generated dotenv line")
				}
				t.Setenv(key, value)
			}
			config := filepath.Join(bundle, "config.toml")
			if role == "executor" {
				t.Setenv("ENCRYPTION_KEY_HEX", "")
				executor, loadErr := backend.LoadExecutorWithArgs(filepath.Join(bundle, "executor.toml"), nil)
				err = loadErr
				if err == nil && executor.Server.Port != 3001 {
					t.Fatal("executor must listen on port 3001")
				}
			} else {
				runtime, loadErr := backend.LoadWithArgs(config, nil)
				err = loadErr
				if err == nil && runtime.Server.Port != 3001 {
					t.Fatal("API must listen on port 3001")
				}
				if err == nil {
					_, err = frontend.LoadWithArgs(config, nil)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
