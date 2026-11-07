package proofreceipts

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var (
	ErrCandidateNotFound     = xerror.NotFound(xerror.Pipeline).Detail("proof candidate not found")
	ErrReceiptNotFound       = xerror.New(xerror.KindNotFound, "PROOF_RECEIPT_NOT_FOUND", "proof receipt not found")
	ErrLifecycleLinkNotFound = xerror.New(xerror.KindNotFound, "PROOF_LIFECYCLE_LINK_NOT_FOUND", "matching proof lifecycle link not found")
)
