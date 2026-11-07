package proofcandidatesvc

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	ErrMappingNotFound  = xerror.New(xerror.KindNotFound, "PROOF_CANDIDATE_MAPPING_NOT_FOUND", "proof candidate mapping not found")
	ErrMappingAmbiguous = xerror.New(xerror.KindInvalidInput, "PROOF_CANDIDATE_MAPPING_AMBIGUOUS", "proof candidate mapping is ambiguous")
	ErrSourceTooLarge   = xerror.New(xerror.KindInvalidInput, "PROOF_CANDIDATE_SOURCE_TOO_LARGE", "proof candidate source is too large")
	ErrParseFailed      = xerror.New(xerror.KindInvalidInput, "PROOF_CANDIDATE_PARSE_FAILED", "proof candidate parse failed")
)
