package eventssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	repoevents "github.com/Braidplane/braidplane-core/postgres/events"
)

var (
	ErrEventNotFound = xerror.NotFound(xerror.Event)
	ErrInvalidInput  = xerror.ValidationFailed()
	ErrSlugTaken     = repoevents.ErrSlugTaken
)
