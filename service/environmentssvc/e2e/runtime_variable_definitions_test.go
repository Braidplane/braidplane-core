package e2e

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/datasourcerefs"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/protectedenv"
)

func TestRuntimeVariableDefinitionsInheritAndStayOrganizationScoped(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres runtime variable definitions test in short mode")
	}
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "runtime variable definitions")
	stack := newDefaultEnvironmentE2EStack(pool)
	org := uniqueEnvironmentDefaultsSlug("vars-org")
	otherOrg := uniqueEnvironmentDefaultsSlug("vars-other-org")
	user := createDefaultEnvironmentE2EOrganization(t, ctx, pool, stack, uniqueEnvironmentDefaultsSlug("vars-user"), org, "Variable Definitions")
	createDefaultEnvironmentE2EOrganization(t, ctx, pool, stack, uniqueEnvironmentDefaultsSlug("vars-other-user"), otherOrg, "Other Organization")
	env := stack.environments
	definition := model.RuntimeVariableDefinition{
		Key: "batch_size", ValueType: model.RuntimeVariableValueTypeInteger, DefaultValue: new("200"), Required: true,
	}
	prod := model.DefaultProductionEnvironmentSlug
	actor := model.UserEventActor(user)
	_, err := env.UpsertRuntimeVariableDefinition(ctx, org, definition, nil, actor)
	require.ErrorIs(t, err, protectedenv.ErrConfirmationRequired)
	_, err = env.UpsertRuntimeVariableDefinition(ctx, org, definition, []slug.Slug{prod}, actor)
	require.NoError(t, err)
	duplicate := definition
	duplicate.CreateOnly = true
	_, err = env.UpsertRuntimeVariableDefinition(ctx, org, duplicate, []slug.Slug{prod}, actor)
	require.ErrorIs(t, err, xerror.AlreadyExists(xerror.RuntimeVariable))

	_, err = env.CreateEnvironment(ctx, org, model.NewEnvironment{Name: "QA", Slug: "qa", Color: model.EnvironmentColorGray}, actor)
	require.NoError(t, err)
	_, err = env.UpsertRuntimeVariable(ctx, org, "dev", model.UpsertRuntimeVariable{
		Key: definition.Key, ValueType: definition.ValueType, Value: "100",
	}, actor)
	require.NoError(t, err)

	_, err = env.UpsertRuntimeVariable(ctx, org, "dev", model.UpsertRuntimeVariable{
		Key: definition.Key, ValueType: model.RuntimeVariableValueTypeText, Value: "wrong type",
	}, actor)
	require.ErrorIs(t, err, environmentssvc.ErrInvalidRuntimeVariable)

	changedType := definition
	changedType.ValueType = model.RuntimeVariableValueTypeText
	_, err = env.UpsertRuntimeVariableDefinition(ctx, org, changedType, []slug.Slug{prod}, actor)
	require.ErrorIs(t, err, environmentssvc.ErrInvalidRuntimeVariable)

	comparison, err := env.CompareRuntimeVariables(ctx, org, []string{"region"})
	require.NoError(t, err)
	require.Len(t, comparison.Rows, 2)
	dev := slices.Index(comparison.Environments, slug.Slug("dev"))
	qa := slices.Index(comparison.Environments, slug.Slug("qa"))
	require.NotEqual(t, -1, dev)
	require.NotEqual(t, -1, qa)
	row := comparison.Rows[0]
	require.Equal(t, definition.Key, row.Key)
	require.Equal(t, model.RuntimeVariableOverride, row.Cells[dev].Source)
	require.Equal(t, "100", row.Cells[dev].Variable.Value)
	require.Equal(t, model.RuntimeVariableDefault, row.Cells[qa].Source)
	require.Equal(t, "200", row.Cells[qa].Variable.Value)
	require.True(t, comparison.Rows[1].Cells[qa].Blocked())

	overrides, definitions, err := datasourcerefs.NewRepository().ListRuntimeVariableResolutionInputs(ctx, pool, org, "qa")
	require.NoError(t, err)
	require.Empty(t, overrides)
	require.Len(t, definitions, 1)
	require.Equal(t, new("200"), definitions[0].DefaultValue)

	other, err := env.CompareRuntimeVariables(ctx, otherOrg, []string{definition.Key})
	require.NoError(t, err)
	require.Len(t, other.Rows, 1)
	require.Nil(t, other.Rows[0].Definition)
	for _, cell := range other.Rows[0].Cells {
		require.Equal(t, model.RuntimeVariableMissing, cell.Source)
		require.True(t, cell.Blocked())
	}

	require.NoError(t, env.DeleteRuntimeVariableDefinition(ctx, org, definition.Key, []slug.Slug{prod}, actor))
	override, err := env.GetRuntimeVariable(ctx, org, "dev", definition.Key)
	require.NoError(t, err)
	require.Equal(t, "100", override.Value)
}
