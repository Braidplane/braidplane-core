package googleworkspace

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

const driveMetadataFields = "id,mimeType,trashed,capabilities(canDownload,canEdit,canAddChildren)"

const metadataFields = "spreadsheetId,sheets(properties(sheetId,gridProperties(rowCount,columnCount)))"

// WorksheetMetadataObserver proves that one configured worksheet still exists
// and contains the declared rectangle. It never reads or writes cell values.
type WorksheetMetadataObserver struct {
	SpreadsheetID string `json:"spreadsheet_id"`
	WorksheetID   int    `json:"worksheet_id"`
	Range         string `json:"range"`
}

type WorksheetMetadataSnapshot struct {
	WorksheetID int `json:"worksheet_id"`
	RowCount    int `json:"row_count"`
	ColumnCount int `json:"column_count"`
}

func (o WorksheetMetadataObserver) Plan() (proofevidence.MetadataPlan, error) {
	if err := workspace.ValidateID(o.SpreadsheetID); err != nil || o.WorksheetID < 0 {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	if _, err := workspace.ParseRange(o.Range); err != nil {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	resource, err := json.Marshal(o)
	if err != nil {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	endpoint := proofevidence.Endpoint{Scheme: "https", Host: "sheets.googleapis.com", Port: 443}
	query := url.Values{"alt": {"json"}, "fields": {metadataFields}, "prettyPrint": {"false"}}.Encode()
	return proofevidence.MetadataPlan{
		ConnectorType: model.DataSourceTypeGoogleWorkspace, Operation: "google_workspace.worksheet_metadata", Resource: resource,
		Target: endpoint, Egress: append([]proofevidence.Endpoint{endpoint}, WorksheetMetadataProviderEndpoints()...),
		Routes: []proofevidence.MetadataRoute{driveMetadataRoute(o.SpreadsheetID), {Method: http.MethodGet, URL: "https://sheets.googleapis.com/v4/spreadsheets/" + o.SpreadsheetID + "?" + query}},
	}, nil
}

func (o WorksheetMetadataObserver) ReadMetadata(ctx context.Context, transport proofevidence.MetadataIO, credential []byte, limits proofevidence.Limits) (json.RawMessage, int64, proofevidence.Outcome) {
	client := transport.HTTPClient()
	if client == nil || client.Transport == nil {
		return nil, 0, proofevidence.OutcomePartial
	}
	var credentials providerCredentials
	if json.Unmarshal(credential, &credentials) != nil || strings.TrimSpace(credentials.AccessToken) == "" {
		return nil, 0, proofevidence.OutcomePartial
	}
	file, err := readDriveMetadata(ctx, transport.HTTPClient(), credentials, o.SpreadsheetID)
	if err != nil || file.MimeType != "application/vnd.google-apps.spreadsheet" {
		return nil, 0, proofevidence.OutcomePartial
	}
	service, err := sheets.NewService(ctx, option.WithLogger(slog.New(slog.DiscardHandler)), option.WithHTTPClient(&http.Client{Transport: &oauth2.Transport{Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: credentials.AccessToken}), Base: client.Transport}, CheckRedirect: client.CheckRedirect}))
	if err != nil {
		return nil, 0, proofevidence.OutcomePartial
	}
	metadata, err := service.Spreadsheets.Get(o.SpreadsheetID).Fields(metadataFields).Context(ctx).Do()
	if err != nil || metadata == nil || metadata.SpreadsheetId != o.SpreadsheetID {
		return nil, 0, proofevidence.OutcomePartial
	}
	rectangle, err := workspace.ParseRange(o.Range)
	if err != nil {
		return nil, 0, proofevidence.OutcomePartial
	}
	for _, sheet := range metadata.Sheets {
		if sheet == nil || sheet.Properties == nil || int(sheet.Properties.SheetId) != o.WorksheetID {
			continue
		}
		grid := sheet.Properties.GridProperties
		if grid == nil || rectangle.EndRow > int(grid.RowCount) || rectangle.EndColumn > int(grid.ColumnCount) {
			return nil, 1, proofevidence.OutcomePartial
		}
		if limits.MaxRows < 1 {
			return nil, 1, proofevidence.OutcomeOverBudget
		}
		payload, err := json.Marshal(WorksheetMetadataSnapshot{WorksheetID: o.WorksheetID, RowCount: int(grid.RowCount), ColumnCount: int(grid.ColumnCount)})
		if err != nil {
			return nil, 1, proofevidence.OutcomePartial
		}
		return payload, 1, proofevidence.OutcomeComplete
	}
	return nil, 1, proofevidence.OutcomePartial
}

type providerCredentials struct {
	AccessToken   string   `json:"access_token"`
	GrantedScopes []string `json:"granted_scopes"`
}

// FileMetadataObserver checks the operation's file or destination folder without downloading content.
type FileMetadataObserver struct {
	Resource  string `json:"resource,omitempty"`
	FileID    string `json:"file_id"`
	Operation string `json:"operation"`
}
type FileMetadataSnapshot struct {
	FileID         string `json:"file_id"`
	MIMEType       string `json:"mime_type"`
	CanDownload    bool   `json:"can_download"`
	CanEdit        bool   `json:"can_edit"`
	CanAddChildren bool   `json:"can_add_children"`
}

func driveEndpoint() proofevidence.Endpoint {
	return proofevidence.Endpoint{Scheme: "https", Host: "www.googleapis.com", Port: 443}
}

func driveMetadataRoute(id string) proofevidence.MetadataRoute {
	query := url.Values{"alt": {"json"}, "fields": {driveMetadataFields}, "prettyPrint": {"false"}, "supportsAllDrives": {"true"}}.Encode()
	return proofevidence.MetadataRoute{Method: http.MethodGet, URL: "https://www.googleapis.com/drive/v3/files/" + id + "?" + query}
}

func (o FileMetadataObserver) Plan() (proofevidence.MetadataPlan, error) {
	if workspace.ValidateID(o.FileID) != nil || o.Operation != "read" && o.Operation != "create" && o.Operation != "update" && o.Operation != "list" && o.Operation != "export" {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	if o.Resource != "" && o.Resource != "file" && o.Resource != "document" && o.Resource != "presentation" || o.Operation == "export" && (o.Resource == "" || o.Resource == "file") || o.Operation == "list" && o.Resource != "" && o.Resource != "file" {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return proofevidence.MetadataPlan{}, proofevidence.ErrAuthorizationDenied
	}
	endpoint := driveEndpoint()
	return proofevidence.MetadataPlan{ConnectorType: model.DataSourceTypeGoogleWorkspace, Operation: "google_workspace.file_metadata", Resource: raw, Target: endpoint, Egress: []proofevidence.Endpoint{endpoint}, Routes: []proofevidence.MetadataRoute{driveMetadataRoute(o.FileID)}}, nil
}

func readDriveMetadata(ctx context.Context, client *http.Client, credential providerCredentials, id string) (*drive.File, error) {
	if credential.AccessToken == "" {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	if !workspace.ValidFileScopes(credential.GrantedScopes) {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	sdk, err := drive.NewService(ctx, option.WithLogger(slog.New(slog.DiscardHandler)), option.WithHTTPClient(&http.Client{Transport: &oauth2.Transport{Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: credential.AccessToken}), Base: client.Transport}, CheckRedirect: client.CheckRedirect}))
	if err != nil {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	file, err := sdk.Files.Get(id).SupportsAllDrives(true).Fields(driveMetadataFields).Context(ctx).Do()
	if err != nil {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	if file == nil || file.Id == "" || id != "root" && file.Id != id || file.Trashed {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	return file, nil
}

func (o FileMetadataObserver) ReadMetadata(ctx context.Context, transport proofevidence.MetadataIO, raw []byte, limits proofevidence.Limits) (json.RawMessage, int64, proofevidence.Outcome) {
	client := transport.HTTPClient()
	if client == nil || client.Transport == nil {
		return nil, 0, proofevidence.OutcomePartial
	}
	var credential providerCredentials
	if json.Unmarshal(raw, &credential) != nil {
		return nil, 0, proofevidence.OutcomePartial
	}
	file, err := readDriveMetadata(ctx, transport.HTTPClient(), credential, o.FileID)
	if err != nil {
		return nil, 0, proofevidence.OutcomePartial
	}
	if limits.MaxRows < 1 {
		return nil, 1, proofevidence.OutcomeOverBudget
	}
	snapshot := FileMetadataSnapshot{FileID: file.Id, MIMEType: file.MimeType}
	if file.Capabilities != nil {
		snapshot.CanDownload = file.Capabilities.CanDownload
		snapshot.CanEdit = file.Capabilities.CanEdit
		snapshot.CanAddChildren = file.Capabilities.CanAddChildren
	}
	if o.Operation != "create" && (workspace.Statement{Resource: o.Resource}).IsNative() && file.MimeType != (workspace.Statement{Resource: o.Resource}).NativeMIMEType() {
		return nil, 1, proofevidence.OutcomePartial
	}
	if (o.Operation == "create" || o.Operation == "list") && file.MimeType != "application/vnd.google-apps.folder" || o.Operation == "create" && !snapshot.CanAddChildren || (o.Operation == "read" && (o.Resource == "" || o.Resource == "file") || o.Operation == "export") && !snapshot.CanDownload || o.Operation == "update" && !snapshot.CanEdit {
		return nil, 1, proofevidence.OutcomePartial
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return nil, 1, proofevidence.OutcomePartial
	}
	return payload, 1, proofevidence.OutcomeComplete
}

func WorksheetMetadataProviderEndpoints() []proofevidence.Endpoint {
	return []proofevidence.Endpoint{driveEndpoint()}
}

func (o WorksheetMetadataObserver) Description() string {
	return "selected spreadsheet worksheet contains the declared rectangle"
}

func (o FileMetadataObserver) Description() string {
	return "operation target metadata is reachable with the connected Workspace grant"
}

func workspaceResourceStatement(config, operation json.RawMessage, source bool) (workspace.Statement, bool) {
	var cfg workspace.Config
	statement, err := workspace.DecodeStatement(operation)
	if err != nil || json.Unmarshal(config, &cfg) != nil {
		return statement, false
	}
	if source {
		return statement, statement.IsRead()
	}
	return statement, statement.Operation == "update" || (statement.Resource == "file" || statement.IsNative()) && statement.Operation == "create"
}

func (o WorksheetMetadataObserver) MatchesResource(config, operation json.RawMessage, source bool) bool {
	stmt, valid := workspaceResourceStatement(config, operation, source)
	return valid && stmt.Resource == "spreadsheet" && stmt.SpreadsheetID == o.SpreadsheetID && stmt.WorksheetID != nil && *stmt.WorksheetID == o.WorksheetID && stmt.Range == o.Range
}

func (o FileMetadataObserver) MatchesResource(config, operation json.RawMessage, source bool) bool {
	stmt, valid := workspaceResourceStatement(config, operation, source)
	id := stmt.FileID
	if stmt.Operation == "create" || stmt.Operation == "list" {
		id = stmt.FolderID
	}
	resource := o.Resource
	if resource == "" {
		resource = "file"
	}
	return valid && stmt.Resource == resource && id == o.FileID && stmt.Operation == o.Operation
}

func (o WorksheetMetadataObserver) AssessMetadata(raw json.RawMessage) (model.ProofVerdict, []model.ProofDiagnostic, error) {
	var snapshot WorksheetMetadataSnapshot
	rectangle, err := workspace.ParseRange(o.Range)
	if err != nil || json.Unmarshal(raw, &snapshot) != nil || snapshot.WorksheetID != o.WorksheetID || snapshot.RowCount < rectangle.EndRow || snapshot.ColumnCount < rectangle.EndColumn {
		return model.ProofVerdictUnverified, nil, proofevidence.ErrAuthorizationDenied
	}
	return model.ProofVerdictPassed, nil, nil
}

func (o FileMetadataObserver) AssessMetadata(raw json.RawMessage) (model.ProofVerdict, []model.ProofDiagnostic, error) {
	var snapshot FileMetadataSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.FileID != o.FileID ||
		o.Operation != "create" && (workspace.Statement{Resource: o.Resource}).IsNative() && snapshot.MIMEType != (workspace.Statement{Resource: o.Resource}).NativeMIMEType() ||
		(o.Operation == "read" && (o.Resource == "" || o.Resource == "file") || o.Operation == "export") && !snapshot.CanDownload || o.Operation == "update" && !snapshot.CanEdit || o.Operation == "create" && (!snapshot.CanAddChildren || snapshot.MIMEType != "application/vnd.google-apps.folder") {
		return model.ProofVerdictUnverified, nil, proofevidence.ErrAuthorizationDenied
	}
	return model.ProofVerdictPassed, nil, nil
}
