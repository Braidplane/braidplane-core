package views

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuilderProposalStatusLabelCoversLifecycleStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		review *BuilderProposalReview
		want   string
	}{
		{name: "unsupported", review: &BuilderProposalReview{}, want: "Provider unsupported"},
		{name: "permission failure", review: &BuilderProposalReview{ProviderSupported: true, DraftState: "publish_failed"}, want: "Publish failed — retry available"},
		{name: "queued", review: &BuilderProposalReview{ProviderSupported: true, DraftState: "publish_queued"}, want: "Publish queued"},
		{name: "superseded", review: &BuilderProposalReview{ProviderSupported: true, ProposalIsStale: true}, want: "Proposal behind draft"},
		{name: "open", review: &BuilderProposalReview{ProviderSupported: true, ProposalState: "open"}, want: "Last known: open"},
		{name: "closed", review: &BuilderProposalReview{ProviderSupported: true, ProposalState: "closed"}, want: "Last known: closed"},
		{name: "merged", review: &BuilderProposalReview{ProviderSupported: true, ProposalState: "merged"}, want: "Last known: merged"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.want, builderProposalStatusLabel(test.review))
		})
	}
}
