package googleworkspace

import (
	"context"
	"encoding/json"
	"net/http"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/googleclient"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/slides/v1"
)

// ReadNative retains the native structure, including every Google Docs tab.
func (c *Client) ReadNative(ctx context.Context, resource, id string, limits Limits) (json.RawMessage, error) {
	if err := c.CheckAccess(false); err != nil {
		return nil, err
	}
	if resource == "document" {
		doc, err := read(ctx, limits, func(ctx context.Context) (*docs.Document, error) {
			return c.docs.Documents.Get(id).IncludeTabsContent(true).Context(ctx).Do()
		}, c.credentials.AccessToken)
		if err != nil {
			return nil, err
		}
		if doc == nil || doc.DocumentId != id {
			return nil, xerror.ExecutionFailed().Detail("Google returned another document")
		}
		return json.Marshal(doc)
	}
	presentation, err := read(ctx, limits, func(ctx context.Context) (*slides.Presentation, error) {
		return c.slides.Presentations.Get(id).Context(ctx).Do()
	}, c.credentials.AccessToken)
	if err != nil {
		return nil, err
	}
	if presentation == nil || presentation.PresentationId != id {
		return nil, xerror.ExecutionFailed().Detail("Google returned another presentation")
	}
	return json.Marshal(presentation)
}

// CreateNative uses Drive so the blank file is created directly in the selected folder.
func (c *Client) CreateNative(ctx context.Context, folder, name, mimeType string, limits Limits) (*workspace.File, error) {
	if err := c.CheckAccess(true); err != nil {
		return nil, err
	}
	ctx, cancel := operationContext(ctx, limits)
	defer cancel()
	f, err := c.drive.Files.Create(&drive.File{Name: name, Parents: []string{folder}, MimeType: mimeType}).SupportsAllDrives(true).Fields(fileFields).Context(ctx).Do()
	if err != nil {
		googleclient.LogError(ctx, err, c.credentials.AccessToken)
		return nil, nativeWriteError(err)
	}
	if f == nil || workspace.ValidateID(f.Id) != nil || f.MimeType != mimeType || f.Name != name {
		return nil, xerror.ExecutionFailed().Detail("Native create outcome is ambiguous; inspect the folder before replay")
	}
	return fileMetadata(f), nil
}

// ReplaceNative exposes only a single literal, case-sensitive replacement request.
func (c *Client) ReplaceNative(ctx context.Context, resource, id, match, text string, limits Limits) (int64, error) {
	if err := c.CheckAccess(true); err != nil {
		return 0, err
	}
	ctx, cancel := operationContext(ctx, limits)
	defer cancel()
	if resource == "document" {
		response, err := c.docs.Documents.BatchUpdate(id, &docs.BatchUpdateDocumentRequest{Requests: []*docs.Request{{ReplaceAllText: &docs.ReplaceAllTextRequest{ContainsText: &docs.SubstringMatchCriteria{Text: match, MatchCase: true}, ReplaceText: text, ForceSendFields: []string{"ReplaceText"}}}}}).Context(ctx).Do()
		if err != nil {
			googleclient.LogError(ctx, err, c.credentials.AccessToken)
			return 0, nativeWriteError(err)
		}
		if response == nil || response.DocumentId != id || len(response.Replies) != 1 || response.Replies[0] == nil || response.Replies[0].ReplaceAllText == nil {
			return 0, xerror.ExecutionFailed().Detail("Native update outcome is ambiguous; inspect before replay")
		}
		return response.Replies[0].ReplaceAllText.OccurrencesChanged, nil
	}
	response, err := c.slides.Presentations.BatchUpdate(id, &slides.BatchUpdatePresentationRequest{Requests: []*slides.Request{{ReplaceAllText: &slides.ReplaceAllTextRequest{ContainsText: &slides.SubstringMatchCriteria{Text: match, MatchCase: true}, ReplaceText: text, ForceSendFields: []string{"ReplaceText"}}}}}).Context(ctx).Do()
	if err != nil {
		googleclient.LogError(ctx, err, c.credentials.AccessToken)
		return 0, nativeWriteError(err)
	}
	if response == nil || response.PresentationId != id || len(response.Replies) != 1 || response.Replies[0] == nil || response.Replies[0].ReplaceAllText == nil {
		return 0, xerror.ExecutionFailed().Detail("Native update outcome is ambiguous; inspect before replay")
	}
	return response.Replies[0].ReplaceAllText.OccurrencesChanged, nil
}

func (c *Client) ExportPDF(ctx context.Context, id string, limits Limits) ([]byte, error) {
	if err := c.CheckAccess(false); err != nil {
		return nil, err
	}
	response, err := read(ctx, limits, func(ctx context.Context) (*http.Response, error) {
		return c.drive.Files.Export(id, "application/pdf").Context(ctx).Download()
	}, c.credentials.AccessToken)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := googleclient.ReadResponse(response.Body, responseLimit(ctx, limits))
	if err != nil {
		googleclient.LogError(ctx, err, c.credentials.AccessToken)
		return nil, safeError(err)
	}
	return body, nil
}

func nativeWriteError(err error) error {
	mapped := safeError(err)
	for _, rejection := range []error{xerror.InvalidCredentials(), xerror.Forbidden(xerror.DataSource), xerror.NotFound(xerror.DataSource), xerror.InvalidInput(xerror.DataSource), xerror.ExecutionRequestSizeExceeded()} {
		if xerror.Is(mapped, rejection) {
			return mapped
		}
	}
	return xerror.ExecutionFailed().Detail("Native write outcome is ambiguous; inspect the target before replay")
}
