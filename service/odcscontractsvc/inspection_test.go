package odcscontractsvc

import (
	"os"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/stretchr/testify/require"
)

func TestInspectionPreservesMeaningAndFailsClosed(t *testing.T) {
	raw, err := os.ReadFile("../../pkg/odcs/testdata/supported.json")
	require.NoError(t, err)
	for _, scenario := range []string{"selected", "unselected", "idless", "wrong-tenant", "tampered", "legacy-secret", "latest", "oversized-selection"} {
		t.Run(scenario, func(t *testing.T) {
			document := raw
			object := "order"
			revision := slug.Slug("v1")
			if scenario == "idless" {
				document = []byte(`{"kind":"DataContract","apiVersion":"v3.2.0","id":"orders","version":"1","schema":[{"name":"ID-less","properties":[{"name":"id","logicalType":"integer"}]}]}`)
			}
			recorded, err := odcs.Record(document, "orders")
			require.NoError(t, err)
			recorded.Slug = revision
			if scenario == "tampered" {
				recorded.Digest = "sha256:tampered"
			}
			if scenario == "legacy-secret" {
				recorded.Document = []byte(strings.Replace(string(document), `"version": "1"`, `"version": "1", "customProperties":[{"property":"password","value":"DO-NOT-DISCLOSE"}]`, 1))
			}
			if scenario == "unselected" || scenario == "idless" {
				object = ""
			}
			if scenario == "latest" {
				revision = "latest"
			}
			if scenario == "oversized-selection" {
				object = strings.Repeat("a", 1025)
			}
			called := false
			repo := fakeRepository{get: func(org, project, contract, gotRevision slug.Slug) (*model.ODCSRevision, error) {
				called = true
				require.Equal(t, slug.Slug("acme"), org)
				require.Equal(t, slug.Slug("etl"), project)
				require.Equal(t, slug.Slug("orders"), contract)
				require.Equal(t, slug.Slug("v1"), gotRevision)
				if scenario == "wrong-tenant" {
					return nil, odcscontracts.ErrNotFound
				}
				return recorded, nil
			}}
			svc := New(testutil.FakeTransactionManager{}, repo, projects.NewRepository(), compiledplans.NewRepository())
			result, err := svc.InspectRevision(t.Context(), "acme", "etl", "orders", revision, object)
			switch scenario {
			case "wrong-tenant", "tampered", "legacy-secret", "latest", "oversized-selection":
				require.Error(t, err)
				require.Nil(t, result)
				require.NotContains(t, err.Error(), "DO-NOT-DISCLOSE")
				if scenario == "latest" || scenario == "oversized-selection" {
					require.False(t, called)
				}
			default:
				require.NoError(t, err)
				require.Equal(t, recorded.Document, result.Revision.Document)
				require.Equal(t, recorded.Digest, result.Projection.Digest)
				require.True(t, result.Projection.CoverageComplete)
				require.True(t, result.Projection.DocumentValid)
				require.NotEmpty(t, result.Projection.Coverage)
				if scenario == "selected" {
					require.NotNil(t, result.Projection.Shape)
				} else {
					require.Nil(t, result.Projection.Shape)
				}
			}
		})
	}
}
