package googleworkspace_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	workspaceconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/googleworkspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorksheetMetadataObserverReadsOnlySelectedWorksheetGrid(t *testing.T) {
	t.Parallel()
	observer := workspaceconnector.WorksheetMetadataObserver{SpreadsheetID: "book", WorksheetID: 7, Range: "A1:B2"}
	plan, err := observer.Plan()
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/drive/v3/files/book" {
			assert.Equal(t, http.MethodGet, r.Method)
			_, _ = w.Write([]byte(`{"id":"book","mimeType":"application/vnd.google-apps.spreadsheet"}`))
			return
		}
		assert.Equal(t, "/v4/spreadsheets/book", r.URL.Path)
		assert.Equal(t, "spreadsheetId,sheets(properties(sheetId,gridProperties(rowCount,columnCount)))", r.URL.Query().Get("fields"))
		assert.Empty(t, r.URL.Query().Get("ranges"))
		assert.Empty(t, r.URL.Query().Get("includeGridData"))
		assert.Equal(t, "Bearer fixture-token", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"spreadsheetId":"book","properties":{"title":"private"},"sheets":[{"properties":{"sheetId":7,"title":"private","gridProperties":{"rowCount":2,"columnCount":2}}}]}`))
	}))
	t.Cleanup(server.Close)
	payload, rows, outcome := observer.ReadMetadata(t.Context(), testutil.ProofMetadataClient(t, plan, server.URL), []byte(`{"access_token":"fixture-token","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`), proofevidence.Limits{MaxRows: 1})
	require.Equal(t, proofevidence.OutcomeComplete, outcome)
	require.EqualValues(t, 1, rows)
	var snapshot workspaceconnector.WorksheetMetadataSnapshot
	require.NoError(t, json.Unmarshal(payload, &snapshot))
	require.Equal(t, workspaceconnector.WorksheetMetadataSnapshot{WorksheetID: 7, RowCount: 2, ColumnCount: 2}, snapshot)
	require.NotContains(t, string(payload), "private")
}

func TestWorksheetMetadataObserverRejectsMissingOrTooSmallGrid(t *testing.T) {
	t.Parallel()
	for _, response := range []string{
		`{"spreadsheetId":"book","sheets":[]}`,
		`{"spreadsheetId":"book","sheets":[{"properties":{"sheetId":7,"gridProperties":{"rowCount":1,"columnCount":2}}}]}`,
	} {
		observer := workspaceconnector.WorksheetMetadataObserver{SpreadsheetID: "book", WorksheetID: 7, Range: "A1:B2"}
		plan, err := observer.Plan()
		require.NoError(t, err)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(response)) }))
		payload, _, outcome := observer.ReadMetadata(t.Context(), testutil.ProofMetadataClient(t, plan, server.URL), []byte(`{"access_token":"fixture-token","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`), proofevidence.Limits{MaxRows: 1})
		server.Close()
		require.Nil(t, payload)
		require.Equal(t, proofevidence.OutcomePartial, outcome)
	}
}

func TestWorksheetMetadataObserverRequiresAdmittedTransport(t *testing.T) {
	t.Parallel()
	observer := workspaceconnector.WorksheetMetadataObserver{SpreadsheetID: "book", WorksheetID: 7, Range: "A1:B2"}
	for _, client := range []*http.Client{nil, {}} {
		payload, rows, outcome := observer.ReadMetadata(t.Context(), testutil.ProofHTTPTransport{Client: client}, []byte(`{"access_token":"fixture-token","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`), proofevidence.Limits{MaxRows: 1})
		require.Nil(t, payload)
		require.Zero(t, rows)
		require.Equal(t, proofevidence.OutcomePartial, outcome)
	}
}

func TestWorkspaceMetadataPlanRejectsCellReadsAndWrites(t *testing.T) {
	for _, route := range []proofevidence.MetadataRoute{
		{Method: http.MethodGet, URL: "https://sheets.googleapis.com/v4/spreadsheets/book/values/A1:B2"},
		{Method: http.MethodPost, URL: "https://sheets.googleapis.com/v4/spreadsheets/book:batchUpdate"},
		{Method: http.MethodGet, URL: "https://sheets.googleapis.com/v4/spreadsheets/book?includeGridData=true"},
	} {
		plan, err := (workspaceconnector.WorksheetMetadataObserver{SpreadsheetID: "book", WorksheetID: 0, Range: "A1:B2"}).Plan()
		require.NoError(t, err)
		plan.Routes[0] = route
		_, err = connector.NewMetadataObserver(plan)
		require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	}
}
