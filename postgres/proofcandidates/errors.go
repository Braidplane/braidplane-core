package proofcandidates

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var ErrCandidateNotFound = xerror.New(
	xerror.KindNotFound,
	"PROOF_CANDIDATE_NOT_FOUND",
	"proof candidate not found",
)

var ErrMappingTargetNotFound = xerror.New(
	xerror.KindNotFound,
	"PROOF_MAPPING_TARGET_NOT_FOUND",
	"proof candidate mapping target not found",
)
