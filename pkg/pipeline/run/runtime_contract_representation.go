package run

import (
	"fmt"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
)

// runtimeRepresentationMismatch reports native file contract mismatches that cannot be structurally normalized.
func runtimeRepresentationMismatch(
	source *StepResult,
	contract *payload.Contract,
) (string, bool) {
	if source == nil || contract == nil ||
		contract.Representation == model.ContractRepresentationUnknown {
		return "", false
	}

	runtimePayload, ok := source.Payload()
	if !ok || runtimePayload == nil ||
		runtimePayload.Representation == model.ContractRepresentationUnknown ||
		runtimePayload.Representation == contract.Representation {
		return "", false
	}
	if runtimePayload.Representation != model.ContractRepresentationFile &&
		contract.Representation != model.ContractRepresentationFile {
		return "", false
	}

	return fmt.Sprintf(
		"runtime payload representation %q does not satisfy contract representation %q",
		runtimePayload.Representation.String(),
		contract.Representation.String(),
	), true
}
