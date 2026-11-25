package authorization

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

// CanCapturedCompletionAuthority evaluates only permission facts read from the scoped completion record.
func CanCapturedCompletionAuthority(ctx context.Context, snapshot model.CompletionAuthoritySnapshot) (bool, error) {
	if !snapshot.Valid || snapshot.PrincipalID == "" {
		return false, nil
	}
	for _, requirement := range []struct {
		action   model.Action
		resource model.Resource
		facts    model.AuthorizationFacts
	}{
		{model.Read, model.ResourcePipelines, snapshot.Scoped.Upstream},
		{model.Update, model.ResourcePipelineTriggers, snapshot.Scoped.Trigger},
		{model.Execute, model.ResourcePipelines, snapshot.Scoped.Downstream},
	} {
		allowed, err := CanWithResolvedFacts(ctx, requirement.action, requirement.resource, requirement.facts)
		if err != nil || !allowed {
			return false, err
		}
	}
	return true, nil
}
