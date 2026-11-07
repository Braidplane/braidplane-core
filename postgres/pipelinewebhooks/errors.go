package pipelinewebhooks

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var ErrReceiverNotFound = xerror.NotFound(xerror.PipelineTrigger)

var ErrDeliveryNotFound = xerror.NotFound(xerror.PipelineRun)
