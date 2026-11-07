package jobs

import (
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type ProofWebhookArgs struct {
	DeliveryID           string                   `json:"delivery_id" river:"unique"`
	BodySHA256           string                   `json:"body_sha256"`
	Action               gitprovider.ChangeAction `json:"action"`
	Provider             repostore.Provider       `json:"provider"`
	OrganizationSlug     slug.Slug                `json:"organization_slug,omitempty"`
	ProviderConnectionID string                   `json:"provider_connection_id"`
	RepositoryID         string                   `json:"repository_id"`
	ChangeRequestID      string                   `json:"change_request_id"`
	HeadSHA              string                   `json:"head_sha"`
	SourceRef            string                   `json:"source_ref"`
	TargetRef            string                   `json:"target_ref"`
}

func (ProofWebhookArgs) Kind() string { return "proof_webhook" }

func (ProofWebhookArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:      PipelineGitQueue,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: rivertype.UniqueOptsByStateDefault()},
	}
}

func (a ProofWebhookArgs) Event() gitprovider.VerifiedChangeEvent {
	return gitprovider.VerifiedChangeEvent{
		DeliveryID: a.DeliveryID, BodySHA256: a.BodySHA256, Action: a.Action, Provider: a.Provider,
		OrganizationSlug:     a.OrganizationSlug,
		ProviderConnectionID: a.ProviderConnectionID, RepositoryID: a.RepositoryID,
		ChangeRequestID: a.ChangeRequestID, HeadSHA: a.HeadSHA,
		SourceRef: a.SourceRef, TargetRef: a.TargetRef,
	}
}
