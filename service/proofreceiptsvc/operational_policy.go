package proofreceiptsvc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
)

const OperationalPolicyVersion = "proof-operational-structural-policy-v1"

type OperationalSelection = model.ProofOperationalSelection

// OperationalObservation contains only retained, safe check aggregates. The
// caller must load it through tenant-scoped storage, never from request data.
type OperationalObservation struct {
	Selection     OperationalSelection       `json:"selection"`
	Status        pipelinerun.CheckStatus    `json:"status"`
	ReasonCode    string                     `json:"reason_code"`
	ExpiresAt     time.Time                  `json:"expires_at"`
	Evidence      *pipelinerun.CheckEvidence `json:"evidence"`
	ReusedFromRun string                     `json:"reused_from_run"`
}

// admitOperationalCheck evaluates evidence only. It grants no execution,
// provider, publication or read authority and assigns no bounded_execution level.
func admitOperationalCheck(expected OperationalSelection, observation *OperationalObservation, now time.Time) (model.ProofVerdict, string, error) {
	if err := validateOperationalSelection(expected); err != nil {
		return model.ProofVerdictUnknown, "", err
	}
	if now.IsZero() {
		return model.ProofVerdictUnknown, "", proofevidence.ErrAuthorizationDenied
	}
	if observation == nil {
		return model.ProofVerdictUnverified, "", nil
	}
	if !reflect.DeepEqual(expected, observation.Selection) {
		return model.ProofVerdictUnknown, "", proofevidence.ErrAuthorizationDenied
	}
	digest, err := digestJSON(struct {
		Policy      string                  `json:"policy"`
		Observation *OperationalObservation `json:"observation"`
	}{OperationalPolicyVersion, observation})
	if err != nil {
		return model.ProofVerdictUnknown, "", err
	}
	if observation.ExpiresAt.IsZero() {
		return model.ProofVerdictUnknown, "", proofevidence.ErrAuthorizationDenied
	}
	if observation.Evidence != nil {
		evidence := observation.Evidence
		if evidence.Rows < 0 || evidence.Rows > 1000000000 || evidence.DurationMs < 0 || evidence.DurationMs > 86400000 || evidence.Empty != (evidence.Rows == 0) ||
			(evidence.BatchScope != "truncated_batch" && evidence.BatchScope != "observed_batch_completeness_unknown") {
			return model.ProofVerdictUnknown, "", proofevidence.ErrAuthorizationDenied
		}
		raw, err := json.Marshal(evidence)
		if err != nil {
			return model.ProofVerdictUnknown, "", err
		}
		sum := sha256.Sum256(raw)
		if expected.EvidenceDigest != hex.EncodeToString(sum[:]) {
			return model.ProofVerdictUnknown, "", proofevidence.ErrAuthorizationDenied
		}
	} else if expected.EvidenceDigest != "" {
		return model.ProofVerdictUnknown, "", proofevidence.ErrAuthorizationDenied
	}
	if expected.Evaluator != "braidplane/structural-runtime/v1" || expected.EvaluatorPolicy != contractplan.StructuralPolicyID {
		return model.ProofVerdictUnverified, digest, nil
	}
	if observation.ReusedFromRun != "" {
		return model.ProofVerdictUnverified, digest, nil
	}
	if observation.Status == pipelinerun.CheckStatusSkipped || observation.Status == pipelinerun.CheckStatusUnavailable {
		return model.ProofVerdictUnverified, digest, nil
	}
	complete := expected.StartedAt != nil && expected.CompletedAt != nil && !expected.StartedAt.IsZero() && !expected.CompletedAt.Before(*expected.StartedAt) && !expected.CompletedAt.After(now) && !observation.ExpiresAt.Before(*expected.CompletedAt) && observation.Evidence != nil
	if complete && !observation.ExpiresAt.After(now) {
		return model.ProofVerdictStale, digest, nil
	}
	switch observation.Status {
	case pipelinerun.CheckStatusSkipped, pipelinerun.CheckStatusUnavailable:
		return model.ProofVerdictUnverified, digest, nil
	case pipelinerun.CheckStatusPassed, pipelinerun.CheckStatusFailed:
		if !complete {
			return model.ProofVerdictInconclusive, digest, nil
		}
		if observation.Status == pipelinerun.CheckStatusPassed {
			if observation.ReasonCode != "conformant" && observation.ReasonCode != "empty_batch" || (observation.ReasonCode == "empty_batch") != observation.Evidence.Empty {
				return model.ProofVerdictInconclusive, digest, nil
			}
			return model.ProofVerdictPassed, digest, nil
		}
		if observation.ReasonCode == "contract_violation" {
			return model.ProofVerdictFailed, digest, nil
		}
	case pipelinerun.CheckStatusError:
		return model.ProofVerdictInconclusive, digest, nil
	}
	return model.ProofVerdictInconclusive, digest, nil
}

func validateOperationalSelection(selection OperationalSelection) error {
	if selection.Validate() != nil {
		return proofevidence.ErrAuthorizationDenied
	}
	return nil
}
