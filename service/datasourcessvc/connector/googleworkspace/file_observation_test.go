package googleworkspace_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	workspaceconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/googleworkspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileMetadataObservationIsMetadataOnlyAndSupportsSharedDrives(t *testing.T) {
	observer := workspaceconnector.FileMetadataObserver{FileID: "shared-file", Operation: "read"}
	plan, err := observer.Plan()
	require.NoError(t, err)
	require.Len(t, plan.Routes, 1)
	require.Equal(t, http.MethodGet, plan.Routes[0].Method)
	require.Contains(t, plan.Routes[0].URL, "supportsAllDrives=true")
	require.NotContains(t, plan.Routes[0].URL, "alt=media")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.NotEqual(t, "media", r.URL.Query().Get("alt"))
		assert.Equal(t, "true", r.URL.Query().Get("supportsAllDrives"))
		_, _ = w.Write([]byte(`{"id":"shared-file","mimeType":"application/json","isAppAuthorized":true,"capabilities":{"canDownload":true}}`))
	}))
	t.Cleanup(server.Close)
	payload, rows, outcome := observer.ReadMetadata(t.Context(), testutil.ProofMetadataClient(t, plan, server.URL), []byte(`{"access_token":"fixture","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`), proofevidence.Limits{MaxRows: 1})
	require.Equal(t, proofevidence.OutcomeComplete, outcome)
	require.EqualValues(t, 1, rows)
	require.NotContains(t, string(payload), "content")
}

func TestFileMetadataObservationRejectsUnprovableWriteAndAuthorization(t *testing.T) {
	for _, tc := range []struct{ name, operation, credential, response string }{
		{"missing scopes rejected", "read", `{"access_token":"fixture"}`, `{"id":"file","mimeType":"application/json","isAppAuthorized":false,"capabilities":{"canDownload":true}}`},
		{"legacy readonly cannot prove create", "create", `{"access_token":"fixture","granted_scopes":["https://www.googleapis.com/auth/drive.readonly"]}`, `{"id":"folder","mimeType":"application/vnd.google-apps.folder","capabilities":{"canAddChildren":true}}`},
		{"legacy broad cannot prove update", "update", `{"access_token":"fixture","granted_scopes":["https://www.googleapis.com/auth/drive"]}`, `{"id":"file","mimeType":"application/json","capabilities":{"canEdit":true}}`},
		{"mismatched stable ID", "update", `{"access_token":"fixture","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`, `{"id":"other-file","mimeType":"application/json","capabilities":{"canEdit":true}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "file"
			if tc.operation == "create" {
				id = "folder"
			}
			observer := workspaceconnector.FileMetadataObserver{FileID: id, Operation: tc.operation}
			plan, err := observer.Plan()
			require.NoError(t, err)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.response)) }))
			t.Cleanup(server.Close)
			payload, _, outcome := observer.ReadMetadata(t.Context(), testutil.ProofMetadataClient(t, plan, server.URL), []byte(tc.credential), proofevidence.Limits{MaxRows: 1})
			require.Nil(t, payload)
			require.Equal(t, proofevidence.OutcomePartial, outcome)
		})
	}
}

func TestFileMetadataObservationWritableFolderCanProveCreate(t *testing.T) {
	observer := workspaceconnector.FileMetadataObserver{FileID: "folder", Operation: "create"}
	plan, err := observer.Plan()
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"folder","mimeType":"application/vnd.google-apps.folder","isAppAuthorized":true,"capabilities":{"canAddChildren":true}}`))
	}))
	t.Cleanup(server.Close)
	payload, _, outcome := observer.ReadMetadata(t.Context(), testutil.ProofMetadataClient(t, plan, server.URL), []byte(`{"access_token":"fixture","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`), proofevidence.Limits{MaxRows: 1})
	require.NotNil(t, payload)
	require.Equal(t, proofevidence.OutcomeComplete, outcome)
}

func TestNativeMetadataObservationNeverReadsContent(t *testing.T) {
	for _, resource := range []string{"document", "presentation"} {
		for _, operation := range []string{"read", "create", "update", "export"} {
			t.Run(resource+operation, func(t *testing.T) {
				observer := workspaceconnector.FileMetadataObserver{Resource: resource, FileID: "native", Operation: operation}
				plan, err := observer.Plan()
				require.NoError(t, err)
				require.Len(t, plan.Routes, 1)
				mime := "application/vnd.google-apps." + resource
				if operation == "create" {
					mime = "application/vnd.google-apps.folder"
				}
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					assert.Equal(t, http.MethodGet, r.Method)
					assert.Equal(t, "/drive/v3/files/native", r.URL.Path)
					assert.NotContains(t, r.URL.Query().Get("fields"), "content")
					assert.NotEqual(t, "media", r.URL.Query().Get("alt"))
					_, _ = w.Write([]byte(`{"id":"native","mimeType":"` + mime + `","capabilities":{"canEdit":true,"canDownload":true,"canAddChildren":true}}`))
				}))
				t.Cleanup(server.Close)
				payload, _, outcome := observer.ReadMetadata(t.Context(), testutil.ProofMetadataClient(t, plan, server.URL), []byte(`{"access_token":"fixture","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`), proofevidence.Limits{MaxRows: 1})
				require.Equal(t, proofevidence.OutcomeComplete, outcome)
				require.Equal(t, 1, calls)
				_, _, err = observer.AssessMetadata(payload)
				require.NoError(t, err)
				fields := `,"file_id":"native"`
				if operation == "create" {
					fields = `,"folder_id":"native","name":"Blank"`
				}
				if operation == "update" {
					fields += `,"match_text":"x","content":"y"`
				}
				raw := []byte(`{"type":"google_workspace","resource":"` + resource + `","operation":"` + operation + `"` + fields + `}`)
				source := operation == "read" || operation == "export"
				require.True(t, observer.MatchesResource([]byte(`{"access_mode":"selected_files"}`), raw, source))
				require.False(t, observer.MatchesResource([]byte(`{"access_mode":"selected_files"}`), raw, !source))
			})
		}
	}
}
