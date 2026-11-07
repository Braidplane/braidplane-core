package projectssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/projects"
)

var (
	ErrProjectNotFound      = projects.ErrNotFound
	ErrSlugTaken            = projects.ErrSlugTaken
	ErrPipelineBackendTaken = projects.ErrPipelineBackendTaken
)

var ErrInvalidProjectSlug = xerror.InvalidInput(xerror.Project)

var ErrRepositorySetupSourceTestRequired = xerror.New(
	xerror.KindInvalidInput,
	"PROJECT_REPOSITORY_SETUP_SOURCE_TEST_REQUIRED",
	"Test the saved repository connection successfully, then resume repository setup. Ask an organization admin if you cannot test connections.",
)

var (
	ErrPipelineBackendDataSourceUnavailable    = xerror.Unavailable(xerror.DataSource)
	ErrPipelineBackendDataSourceTypeNotAllowed = xerror.InvalidInput(xerror.DataSource)
	ErrPipelineBackendReadOnly                 = xerror.InvalidInput(xerror.DataSource)
	ErrPipelineBackendTrackedBranchInvalid     = xerror.InvalidInput(xerror.Project)
	ErrPipelineBackendDataSourceChanged        = xerror.New(
		xerror.KindInvalidInput,
		"PROJECT_PIPELINE_BACKEND_DATA_SOURCE_CHANGED",
		"project pipeline backend data source changed",
	)
	ErrPipelineBackendReconcileFailed = xerror.New(
		xerror.KindInvalidInput,
		"PROJECT_PIPELINE_BACKEND_RECONCILE_FAILED",
		"project pipeline backend reconcile failed",
	)
)
