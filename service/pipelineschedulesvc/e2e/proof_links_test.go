package e2e

import (
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
)

func newTestProofLinks() proofreceipts.LifecycleRepository { return proofreceipts.New() }
