package authorization

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// CanCapturedCompletionAuthority evaluates only permission facts read from the scoped completion record.
func CanCapturedCompletionAuthority(ctx context.Context, org slug.Slug, snapshot model.CompletionAuthoritySnapshot) (bool, error) {
	scoped := snapshot.Scoped
	if !snapshot.Valid || org == "" || snapshot.PrincipalID == "" || scoped == nil {
		return false, nil
	}
	for _, requirement := range []struct {
		action   model.Action
		resource model.Resource
		facts    model.AuthorizationFacts
	}{
		{model.Read, model.ResourcePipelines, scoped.Upstream},
		{model.Update, model.ResourcePipelineTriggers, scoped.Trigger},
		{model.Execute, model.ResourcePipelines, scoped.Downstream},
	} {
		allowed, err := CanWithResolvedFacts(ctx, requirement.action, requirement.resource, requirement.facts)
		if err != nil || !allowed {
			return false, err
		}
	}
	return true, nil
}
