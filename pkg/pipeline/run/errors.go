package run

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	ErrNilPipeline                       = xerror.InvalidRequired(xerror.Pipeline)
	ErrContractMismatch                  = xerror.PipelineContractMismatch()
	ErrQueryOutputContractSchemaMismatch = xerror.QueryOutputContractSchemaMismatch()
)
