package proofevidence

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/stretchr/testify/require"
)

func TestMetadataPlanBindsResourceAndRejectsUnadmittedRoutes(t *testing.T) {
	t.Parallel()
	endpoint := Endpoint{Scheme: "https", Host: "warehouse.example.com", Port: 443}
	plan := MetadataPlan{ConnectorType: model.DataSourceTypeBigQuery, Operation: "table.metadata", Resource: json.RawMessage(`{"table":"one"}`), Target: endpoint, Egress: []Endpoint{endpoint}, Routes: []MetadataRoute{{Method: http.MethodGet, URL: "https://warehouse.example.com/tables/one"}}}
	original, err := plan.Fingerprint()
	require.NoError(t, err)
	plan.Resource = json.RawMessage(`{"table":"two"}`)
	changed, err := plan.Fingerprint()
	require.NoError(t, err)
	require.NotEqual(t, original, changed)
	plan.Routes[0].URL = "https://warehouse.example.com/tables/one?fields=id"
	withQuery, err := plan.Fingerprint()
	require.NoError(t, err)
	require.NotEqual(t, original, withQuery)
	for _, address := range []string{"https://attacker.example/tables/one", "http://warehouse.example.com/tables/one", "https://user@warehouse.example.com/tables/one", "https://warehouse.example.com:8443/tables/one", "https://warehouse.example.com/tables/one#fragment"} {
		plan.Routes[0].URL = address
		_, err := plan.Fingerprint()
		require.ErrorIs(t, err, ErrAuthorizationDenied, address)
	}
	plan.Routes[0] = MetadataRoute{Method: http.MethodDelete, URL: "https://warehouse.example.com/tables/one"}
	_, err = plan.Fingerprint()
	require.ErrorIs(t, err, ErrAuthorizationDenied)
}
