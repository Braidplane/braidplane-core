package jobs

import (
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Braidplane/braidplane-core/pkg/uuid"
)

const ExecutionQueue = "execution"

type ExecutionRequestArgs struct {
	RequestID uuid.UUID `json:"request_id" river:"unique"`
}

func (ExecutionRequestArgs) Kind() string { return "execution_request" }

func (ExecutionRequestArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: ExecutionQueue,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: rivertype.UniqueOptsByStateDefault(),
		},
	}
}
