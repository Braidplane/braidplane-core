package pipelinewebhooksvc

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	ErrInvalidDelivery    = xerror.InvalidInput(xerror.PipelineTrigger)
	ErrVerificationFailed = xerror.New(xerror.KindUnauthorized, "PIPELINE_WEBHOOK_VERIFICATION_FAILED", "pipeline webhook verification failed")
	ErrDeliveryConflict   = xerror.New(xerror.KindAlreadyExists, "PIPELINE_WEBHOOK_DELIVERY_CONFLICT", "pipeline webhook delivery identity conflict")
	ErrReplayUnavailable  = xerror.New(xerror.KindInvalidInput, "PIPELINE_WEBHOOK_REPLAY_UNAVAILABLE", "pipeline webhook delivery replay is unavailable")
)
