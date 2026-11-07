package jobs

import (
	"slices"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Only protected receipt selectors enter the queue, never evidence or provider credentials.
type ProofPublicationArgs struct {
	Organization slug.Slug `json:"organization"`
	Project      slug.Slug `json:"project"`
	Environment  slug.Slug `json:"environment"`
	Receipt      slug.Slug `json:"receipt"`
}

func (ProofPublicationArgs) Kind() string { return "proof_publication" }

func (ProofPublicationArgs) InsertOpts() river.InsertOpts {
	states := slices.DeleteFunc(rivertype.UniqueOptsByStateDefault(), func(state rivertype.JobState) bool { return state == rivertype.JobStateCompleted })
	return river.InsertOpts{Queue: PipelineGitQueue, MaxAttempts: 10, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: states}}
}
