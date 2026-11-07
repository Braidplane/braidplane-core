package main

import (
	"fmt"
	"io"

	"github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type dataSourceCapabilitiesCommand struct {
	cliOutputOptions
}

func (c *dataSourceCapabilitiesCommand) Run(output *cliOutput) error {
	return writeCLIOutput(output.stdout, dataSourceCapabilitiesResult{
		Connectors:   spec.AllConnectorCapabilities(),
		SupportGuide: "connectors/support-levels/",
		Scope:        "This build's declared capabilities; connection permissions and operation evidence still apply. Unlisted operations and payloads are unsupported. Load modes describe operation semantics, not new request parameters. See the support guide for maintenance ownership and provider verification.",
	}, c.Output)
}

type dataSourceCapabilitiesResult struct {
	Connectors   []spec.ConnectorCapability `json:"connectors"`
	SupportGuide string                     `json:"support_guide"`
	Scope        string                     `json:"scope"`
}

func (r dataSourceCapabilitiesResult) JSONValue() any { return r }

func (r dataSourceCapabilitiesResult) WriteCLIText(writer io.Writer) error {
	for _, capability := range r.Connectors {
		if _, err := fmt.Fprintf(writer, "%s: %s\n", capability.DataSourceType, capability.SupportSummary()); err != nil {
			return xerror.Unexpected().With(err)
		}
		for _, detail := range capability.CapabilityDetails() {
			if _, err := fmt.Fprintf(writer, "  %s\n", detail); err != nil {
				return xerror.Unexpected().With(err)
			}
		}
	}
	_, err := fmt.Fprintf(writer, "%s\nDocumentation: %s\n", r.Scope, r.SupportGuide)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}
