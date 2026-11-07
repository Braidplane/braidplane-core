package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDataSourceCapabilitiesWithoutServerOrCredentials(t *testing.T) {
	for _, format := range []string{"json", "yaml", "text"} {
		t.Run(format, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runWithRuntime([]string{"braidplane", "data-source", "capabilities", "-o", format},
				&stdout, &stderr, cliRuntime{configPath: filepath.Join(t.TempDir(), "absent.toml")})
			require.NoError(t, err)
			require.Empty(t, stderr.String())
			switch format {
			case "json":
				var result dataSourceCapabilitiesResult
				require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
				require.Equal(t, spec.AllConnectorCapabilities(), result.Connectors)
				require.Equal(t, "connectors/support-levels/", result.SupportGuide)
			case "yaml":
				var result map[string]any
				require.NoError(t, yaml.Unmarshal(stdout.Bytes(), &result))
				require.Equal(t, "connectors/support-levels/", result["support_guide"])
				require.Len(t, result["connectors"], len(spec.AllConnectorCapabilities()))
			default:
				for _, capability := range spec.AllConnectorCapabilities() {
					require.Contains(t, stdout.String(), capability.DataSourceType.String()+": "+capability.SupportSummary())
					for _, detail := range capability.CapabilityDetails() {
						require.Contains(t, stdout.String(), detail)
					}
				}
			}
		})
	}
}
