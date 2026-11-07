package wishlistregistrations

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

type UpsertParams struct {
	WorkEmail                   string
	CareMostAbout               model.WishlistFocus
	CommunicationRequestVersion string
	WorkflowToTestFirst         string
}

type WishlistRegistration struct {
	WorkEmail                   string
	CareMostAbout               model.WishlistFocus
	CommunicationRequestVersion string
	WorkflowToTestFirst         string
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
}
