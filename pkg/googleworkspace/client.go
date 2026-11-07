// Package googleworkspace applies Braidplane execution policies to Google's official clients.
package googleworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/googleclient"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"golang.org/x/oauth2"
	"google.golang.org/api/docs/v1"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
	"google.golang.org/api/slides/v1"
)

const (
	defaultTimeout          = 30 * time.Second
	defaultMaxResponseBytes = googleclient.DefaultMaxResponseBytes
	maxRequestBytes         = 2_000_000
)

// Limits bound one provider operation.
type Limits struct {
	Timeout          time.Duration
	MaxResponseBytes int64
	MaxRequestBytes  int64
	RetryAttempts    int
	RetryBackoff     time.Duration
}

// Credentials holds the short-lived OAuth access token supplied at runtime.
type Credentials struct {
	AccessToken   string   `json:"access_token" validate:"required"`
	GrantedScopes []string `json:"granted_scopes"`
}

// Client wraps the official Drive and Sheets services.
type Client struct {
	client      *http.Client
	sheets      *sheets.Service
	drive       *drive.Service
	docs        *docs.Service
	slides      *slides.Service
	credentials Credentials
}

type limitsKey struct{}

// New uses only the runtime token; the SDK must not discover ambient credentials.
func New(ctx context.Context, creds Credentials) (*Client, error) {
	if strings.TrimSpace(creds.AccessToken) == "" {
		return nil, xerror.InvalidCredentials()
	}
	base := googleclient.NewHTTPClient(0, http.DefaultTransport)
	client := *base
	client.Transport = &oauth2.Transport{Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: creds.AccessToken}), Base: &boundedTransport{client: base}}
	// SDK debug logging can contain row data. Connector tracing owns diagnostics.
	opts := []option.ClientOption{option.WithHTTPClient(&client), option.WithLogger(slog.New(slog.DiscardHandler))}
	sheetService, err := sheets.NewService(ctx, opts...)
	if err != nil {
		return nil, xerror.ExecutionFailed().With(err).Detail("Cannot initialize Google Sheets client")
	}
	driveService, err := drive.NewService(ctx, opts...)
	if err != nil {
		return nil, xerror.ExecutionFailed().With(err).Detail("Cannot initialize Google Drive client")
	}
	docService, err := docs.NewService(ctx, opts...)
	if err != nil {
		return nil, xerror.ExecutionFailed().With(err).Detail("Cannot initialize Google Docs client")
	}
	slideService, err := slides.NewService(ctx, opts...)
	if err != nil {
		return nil, xerror.ExecutionFailed().With(err).Detail("Cannot initialize Google Slides client")
	}
	return &Client{client: base, sheets: sheetService, drive: driveService, docs: docService, slides: slideService, credentials: creds}, nil
}

// Close releases idle HTTP connections.
func (c *Client) Close() error {
	if c != nil && c.client != nil {
		c.client.CloseIdleConnections()
	}
	return nil
}

// Metadata reads spreadsheet and worksheet metadata.
func (c *Client) Metadata(ctx context.Context, id string, limits Limits) (*workspace.Metadata, error) {
	file, err := c.FileMetadata(ctx, id, limits)
	if err != nil {
		return nil, err
	}
	if file.MIMEType != "application/vnd.google-apps.spreadsheet" {
		return nil, xerror.ValidationFailed().Detail("Choose a Google spreadsheet")
	}
	response, err := read(ctx, limits, func(ctx context.Context) (*sheets.Spreadsheet, error) {
		return c.sheets.Spreadsheets.Get(id).Fields("spreadsheetId,properties(title),sheets(properties(sheetId,title,gridProperties(rowCount,columnCount)))").Context(ctx).Do()
	}, c.credentials.AccessToken)
	if err != nil {
		return nil, err
	}
	if response == nil || response.SpreadsheetId != id {
		return nil, xerror.ExecutionFailed().Detail("Google returned unexpected spreadsheet metadata")
	}
	result := &workspace.Metadata{SpreadsheetID: response.SpreadsheetId, Worksheets: []workspace.Worksheet{}}
	if response.Properties != nil {
		result.Title = response.Properties.Title
	}
	for _, sheet := range response.Sheets {
		if sheet == nil || sheet.Properties == nil {
			return nil, xerror.ExecutionFailed().Detail("Invalid worksheet metadata")
		}
		props := sheet.Properties
		item := workspace.Worksheet{WorksheetID: int(props.SheetId), Title: props.Title}
		if props.GridProperties != nil {
			item.RowCount, item.ColumnCount = int(props.GridProperties.RowCount), int(props.GridProperties.ColumnCount)
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, xerror.ExecutionFailed().Detail("Invalid worksheet metadata")
		}
		if err := schemaquota.Charge(ctx, 1, len(encoded)); err != nil {
			return nil, err
		}
		result.Worksheets = append(result.Worksheets, item)
	}
	return result, nil
}

const fileFields = "id,name,mimeType,driveId,trashed,capabilities(canDownload,canEdit,canAddChildren)"

func (c *Client) CheckAccess(_ bool) error {
	if workspace.ValidFileScopes(c.credentials.GrantedScopes) {
		return nil
	}
	return xerror.Forbidden(xerror.DataSource).Detail("Reconnect Google Workspace with selected-file access")
}

func (c *Client) TestConnection(ctx context.Context, limits Limits) error {
	if err := c.CheckAccess(false); err != nil {
		return err
	}
	_, err := read(ctx, limits, func(ctx context.Context) (*drive.About, error) {
		return c.drive.About.Get().Fields("user(emailAddress)").Context(ctx).Do()
	}, c.credentials.AccessToken)
	return err
}

func (c *Client) FileMetadata(ctx context.Context, id string, limits Limits) (*workspace.File, error) {
	if err := workspace.ValidateID(id); err != nil {
		return nil, xerror.Wrapf("validate Drive file ID: %w", err)
	}
	if err := c.CheckAccess(false); err != nil {
		return nil, err
	}
	f, err := read(ctx, limits, func(ctx context.Context) (*drive.File, error) {
		return c.drive.Files.Get(id).SupportsAllDrives(true).Fields(fileFields).Context(ctx).Do()
	}, c.credentials.AccessToken)
	if err != nil {
		return nil, err
	}
	if f == nil || f.Id == "" || f.Trashed {
		return nil, xerror.NotFound(xerror.DataSource)
	}
	if id != "root" && f.Id != id {
		return nil, xerror.ExecutionFailed().Detail("Google returned metadata for another file")
	}
	return fileMetadata(f), nil
}

func fileMetadata(f *drive.File) *workspace.File {
	result := &workspace.File{FileID: f.Id, Name: f.Name, MIMEType: f.MimeType, DriveID: f.DriveId}
	if f.Capabilities != nil {
		result.CanDownload = f.Capabilities.CanDownload
		result.CanEdit = f.Capabilities.CanEdit
		result.CanAddChildren = f.Capabilities.CanAddChildren
	}
	return result
}

// Discovery returns one bounded page of files accessible to the connected account.
func (c *Client) Discovery(ctx context.Context, options workspace.ListOptions, limits Limits, maxItems int) (*workspace.Discovery, error) {
	if err := options.Validate(); err != nil {
		return nil, xerror.Wrapf("validate Drive list options: %w", err)
	}
	if err := c.CheckAccess(false); err != nil {
		return nil, err
	}
	size := 100
	if maxItems > 0 && maxItems < size {
		size = maxItems
	}
	q := "trashed=false"
	if options.FolderID != "" {
		q += " and '" + options.FolderID + "' in parents"
	}
	if options.MIMEType != "" {
		q += " and mimeType='" + strings.ReplaceAll(strings.ReplaceAll(options.MIMEType, "\\", "\\\\"), "'", "\\'") + "'"
	}
	response, err := read(ctx, limits, func(ctx context.Context) (*drive.FileList, error) {
		call := c.drive.Files.List().Q(q).Spaces("drive").Corpora("user").SupportsAllDrives(true).IncludeItemsFromAllDrives(true).PageSize(int64(size)).Fields("nextPageToken,files(" + fileFields + ")").PageToken(options.PageToken)
		if options.DriveID != "" {
			call = call.Corpora("drive").DriveId(options.DriveID)
		}
		return call.Context(ctx).Do()
	}, c.credentials.AccessToken)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, xerror.ExecutionFailed().Detail("Invalid Drive discovery response")
	}
	if len(response.Files) > size {
		return nil, xerror.ExecutionRowLimitExceeded()
	}
	result := &workspace.Discovery{Files: []workspace.File{}, NextPageToken: response.NextPageToken}
	for _, f := range response.Files {
		if f == nil || f.Id == "" {
			return nil, xerror.ExecutionFailed().Detail("Invalid Drive file metadata")
		}
		if f.Trashed {
			continue
		}
		item := fileMetadata(f)
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		if err := schemaquota.Charge(ctx, 1, len(encoded)); err != nil {
			return nil, err
		}
		result.Files = append(result.Files, *item)
	}
	return result, nil
}

func (c *Client) Download(ctx context.Context, id string, limits Limits) ([]byte, error) {
	response, err := read(ctx, limits, func(ctx context.Context) (*http.Response, error) {
		return c.drive.Files.Get(id).SupportsAllDrives(true).Context(ctx).Download()
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

// Upload has one provider attempt. A transport failure after dispatch is ambiguous.
func (c *Client) Upload(ctx context.Context, id, folder, name, contentType string, content []byte, limits Limits) (*workspace.File, error) {
	if err := c.CheckAccess(true); err != nil {
		return nil, err
	}
	if int64(len(content)) > requestLimit(limits) {
		return nil, xerror.ExecutionRequestSizeExceeded()
	}
	ctx, cancel := operationContext(ctx, limits)
	defer cancel()
	var f *drive.File
	var err error
	if id == "" {
		f, err = c.drive.Files.Create(&drive.File{Name: name, Parents: []string{folder}, MimeType: contentType}).SupportsAllDrives(true).Fields(fileFields).Media(bytes.NewReader(content), googleapi.ContentType(contentType)).Context(ctx).Do()
	} else {
		f, err = c.drive.Files.Update(id, &drive.File{}).SupportsAllDrives(true).Fields(fileFields).Media(bytes.NewReader(content), googleapi.ContentType(contentType)).Context(ctx).Do()
	}
	if err != nil {
		mapped := safeError(err)
		googleclient.LogError(ctx, err, c.credentials.AccessToken)
		if ctx.Err() != nil {
			mapped = xerror.Timeout(xerror.DataSource)
		}
		if xerror.Is(mapped, xerror.Unavailable(xerror.DataSource)) || xerror.Is(mapped, xerror.Timeout(xerror.DataSource)) || xerror.Is(mapped, xerror.ExecutionResponseSizeExceeded()) || xerror.Is(mapped, xerror.ExecutionFailed()) {
			return nil, xerror.Wrapf("Drive write outcome is ambiguous; inspect the destination before retrying: %w", mapped)
		}
		return nil, mapped
	}
	if f == nil || f.Id == "" || id != "" && f.Id != id {
		return nil, xerror.ExecutionFailed().Detail("Drive write outcome is ambiguous; inspect before retrying")
	}
	return fileMetadata(f), nil
}

func (c *Client) Read(ctx context.Context, id, cellRange string, limits Limits) ([][]any, error) {
	if err := c.CheckAccess(false); err != nil {
		return nil, err
	}
	response, err := read(ctx, limits, func(ctx context.Context) (*sheets.ValueRange, error) {
		return c.sheets.Spreadsheets.Values.Get(id, cellRange).MajorDimension("ROWS").ValueRenderOption("UNFORMATTED_VALUE").DateTimeRenderOption("SERIAL_NUMBER").Context(ctx).Do()
	}, c.credentials.AccessToken)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, xerror.ExecutionFailed().Detail("Invalid Google Sheets values response")
	}
	return response.Values, nil
}

// Update overwrites one exact rectangle. A dispatched write is never retried.
func (c *Client) Update(ctx context.Context, id, cellRange string, values [][]any, limits Limits) error {
	if err := c.CheckAccess(true); err != nil {
		return err
	}
	ctx, cancel := operationContext(ctx, limits)
	defer cancel()
	payload := &sheets.ValueRange{Range: cellRange, MajorDimension: "ROWS", Values: values}
	_, err := c.sheets.Spreadsheets.Values.Update(id, cellRange, payload).ValueInputOption("RAW").IncludeValuesInResponse(false).Context(ctx).Do()
	if err != nil {
		googleclient.LogError(ctx, err, c.credentials.AccessToken)
		var provider *googleapi.Error
		if xerror.As(err, &provider) && provider.Code < 500 && provider.Code != http.StatusTooManyRequests {
			return googleclient.StatusError(provider.Code)
		}
		for _, rejection := range []error{xerror.InvalidCredentials(), xerror.Forbidden(xerror.DataSource), xerror.NotFound(xerror.DataSource), xerror.InvalidInput(xerror.DataSource)} {
			if xerror.Is(err, rejection) {
				return rejection
			}
		}
		if xerror.Is(err, xerror.ExecutionRequestSizeExceeded()) {
			return xerror.ExecutionRequestSizeExceeded().Detail("Google Sheets update request exceeds byte limit")
		}
		// Serialization failures occur before HTTP dispatch.
		var unsupported *json.UnsupportedTypeError
		var invalid *json.UnsupportedValueError
		if xerror.As(err, &unsupported) || xerror.As(err, &invalid) {
			return xerror.ValidationFailed().Detail("Google Sheets update cells must be JSON scalars")
		}
		return xerror.ExecutionFailed().Detail("Google Sheets update outcome is unknown")
	}
	return nil
}

func operationContext(ctx context.Context, limits Limits) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithValue(ctx, limitsKey{}, limits), timeout(limits))
}

// The generated clients issue a single request per Do call. Only reads enter this retry loop.
func read[T any](ctx context.Context, limits Limits, call func(context.Context) (T, error), sensitiveValues ...string) (T, error) {
	ctx, cancel := operationContext(ctx, limits)
	defer cancel()
	attempts := max(1, limits.RetryAttempts)
	for attempt := 0; ; attempt++ {
		result, err := call(ctx)
		if err == nil {
			return result, nil
		}
		googleclient.LogError(ctx, err, sensitiveValues...)
		if ctx.Err() != nil {
			return result, xerror.Timeout(xerror.DataSource)
		}
		retryAfter, retry := retryable(err)
		if !retry || attempt+1 >= attempts {
			return result, safeError(err)
		}
		if wait(ctx, retryDelay(limits, retryAfter)) != nil {
			return result, xerror.Timeout(xerror.DataSource)
		}
	}
}

// Bound bytes before the SDK decodes responses or sends a serialized update.
type boundedTransport struct{ client *http.Client }

func (t *boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	limits, _ := req.Context().Value(limitsKey{}).(Limits)
	if req.Body != nil {
		body, err := io.ReadAll(io.LimitReader(req.Body, requestLimit(limits)+1))
		_ = req.Body.Close()
		if err != nil {
			return nil, xerror.ExecutionFailed().Detail("Cannot read Google update request")
		}
		if int64(len(body)) > requestLimit(limits) {
			return nil, xerror.ExecutionRequestSizeExceeded()
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	response, err := t.client.Transport.RoundTrip(req)
	if err != nil {
		return nil, xerror.Unavailable(xerror.DataSource).With(err)
	}
	if stream, _ := req.Context().Value(streamDownloadKey{}).(bool); stream && response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}
	body, err := googleclient.ReadResponse(response.Body, responseLimit(req.Context(), limits))
	_ = response.Body.Close()
	if err != nil {
		if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
			return nil, googleclient.StatusError(response.StatusCode)
		}
		if transientTransport(err) && (response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500) {
			return nil, &googleapi.Error{Code: response.StatusCode, Header: response.Header}
		}
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}

func retryable(err error) (string, bool) {
	var provider *googleapi.Error
	if xerror.As(err, &provider) {
		retry := provider.Code == http.StatusTooManyRequests || provider.Code >= 500
		if provider.Code == http.StatusForbidden {
			for _, item := range provider.Errors {
				if item.Reason == "rateLimitExceeded" || item.Reason == "userRateLimitExceeded" {
					retry = true
				}
			}
		}
		return provider.Header.Get("Retry-After"), retry
	}
	// A transport failure carries a URL error. Decoder/validation failures do not.
	var transport *url.Error
	return "", xerror.As(err, &transport) && transientTransport(err)
}

func safeError(err error) error {
	var provider *googleapi.Error
	if xerror.As(err, &provider) {
		return googleclient.StatusError(provider.Code)
	}
	for _, known := range []error{xerror.ExecutionRequestSizeExceeded(), xerror.ExecutionResponseSizeExceeded(), xerror.InvalidCredentials(), xerror.Forbidden(xerror.DataSource), xerror.NotFound(xerror.DataSource), xerror.InvalidInput(xerror.DataSource)} {
		if xerror.Is(err, known) {
			return known
		}
	}
	if transientTransport(err) {
		return xerror.Unavailable(xerror.DataSource)
	}
	return xerror.ExecutionFailed().Detail("Invalid or interrupted Google provider response")
}

func timeout(l Limits) time.Duration {
	if l.Timeout > 0 {
		return l.Timeout
	}
	return defaultTimeout
}

func requestLimit(l Limits) int64 {
	if l.MaxRequestBytes > 0 && l.MaxRequestBytes < maxRequestBytes {
		return l.MaxRequestBytes
	}
	return maxRequestBytes
}

func responseLimit(ctx context.Context, l Limits) int64 {
	limit := int64(defaultMaxResponseBytes)
	if l.MaxResponseBytes > 0 {
		limit = l.MaxResponseBytes
	}
	remaining := schemaquota.RemainingBytes(ctx, int(limit))
	if remaining <= 0 {
		return 0
	}
	return int64(remaining)
}

func retryDelay(l Limits, retryAfter string) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(retryAfter), 10, 64); err == nil && seconds >= 0 {
		if seconds > int64((1<<63-1)/time.Second) {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(retryAfter); err == nil {
		if delay := time.Until(date); delay > 0 {
			return delay
		}
	}
	if l.RetryBackoff > 0 {
		return l.RetryBackoff
	}
	return 0
}

func wait(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func transientTransport(err error) bool {
	if xerror.Is(err, io.EOF) || xerror.Is(err, io.ErrUnexpectedEOF) || xerror.Is(err, syscall.ECONNRESET) || xerror.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var transport *url.Error
	if xerror.As(err, &transport) {
		return transientTransport(transport.Err)
	}
	var networkError net.Error
	return xerror.As(err, &networkError) && networkError.Timeout()
}
