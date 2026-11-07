package datasourcerefssvc

import (
	"fmt"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/datasourcerefs"
)

var (
	ErrDataSourceRefNotFound        = datasourcerefs.ErrDataSourceRefNotFound
	ErrDataSourceRefSlugTaken       = datasourcerefs.ErrDataSourceRefSlugTaken
	ErrDataSourceBindingNotFound    = datasourcerefs.ErrDataSourceBindingNotFound
	ErrProjectEnvironmentNotEnabled = datasourcerefs.ErrProjectEnvironmentNotEnabled

	ErrDataSourceRefIncompatible    = xerror.DataSourceRefIncompatible()
	ErrDataSourceBindingUnavailable = xerror.DataSourceBindingUnavailable()
	ErrInvalidDataSourceRefInput    = xerror.ValidationFailed()
)

type ResolutionDiagnostic struct {
	Field                  string
	StepSlug               *slug.Slug
	DataSourceRefSlug      slug.Slug
	DataSourceRef          slug.Slug
	ResolvedDataSourceSlug slug.Slug
	EnvironmentSlug        slug.Slug
	DataSourceType         model.DataSourceType
	ExpectedDataSourceType model.DataSourceType
	RequiredCapability     model.DataSourceRefCapability
}

type resolutionError struct {
	err        error
	message    string
	diagnostic ResolutionDiagnostic
}

func (e *resolutionError) Error() string {
	if e == nil {
		return ""
	}
	if e.message == "" {
		return e.err.Error()
	}
	var xe *xerror.Error
	if xerror.As(e.err, &xe) {
		return xe.Detail(e.message).Error()
	}
	return fmt.Sprintf("%s: %s", e.err.Error(), e.message)
}

func (e *resolutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func AsResolutionDiagnostic(err error) (ResolutionDiagnostic, bool) {
	var resolution *resolutionError
	if xerror.As(err, &resolution) {
		return resolution.diagnostic, true
	}
	return ResolutionDiagnostic{}, false
}
