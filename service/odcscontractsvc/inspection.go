package odcscontractsvc

import (
	"context"
	"unicode/utf8"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type Inspection struct {
	Revision   *model.ODCSRevision
	Projection *odcs.ConversionResult
}

func (s *service) InspectRevision(ctx context.Context, org, project, contract, revision slug.Slug, object string) (*Inspection, error) {
	if revision.String() == "latest" || len(object) > 1024 || !utf8.ValidString(object) {
		return nil, xerror.ValidationFailed()
	}
	stored, err := s.GetRevision(ctx, org, project, contract, revision)
	if err != nil {
		return nil, err
	}
	projection, err := odcs.Convert(stored.Document, object)
	if err != nil {
		return nil, err
	}
	// Convert rechecks current admission; legacy unsafe content must stay undisclosed.
	if !projection.DocumentValid {
		return nil, odcs.ErrDocument
	}
	if projection.Digest != stored.Digest || stored.ODCSVersion != odcs.PinnedVersion {
		return nil, xerror.Unexpected().Detail("ODCS inspection document integrity mismatch")
	}
	return &Inspection{Revision: stored, Projection: projection}, nil
}
