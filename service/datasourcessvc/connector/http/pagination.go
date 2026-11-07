package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/param"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

type paginationCursor struct{ name, value, targetURL string }

type httpPagination struct {
	PageNumberPath string `json:"page_number_path"`
	PageParam      string `json:"page_param"`
	PageSizeParam  string `json:"page_size_param"`
	PageSize       int    `json:"page_size"`
	TotalPagesPath string `json:"total_pages_path"`
	CursorParam    string `json:"cursor_param"`
	RecordsPath    string `json:"records_path"`
	NextCursorPath string `json:"next_cursor_path"`
	NextLinkPath   string `json:"next_link_path"`
	MaxPages       int    `json:"max_pages"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func invalidPagination(detail string) *xerror.Error {
	return xerror.InvalidInput(xerror.Query).Detail(detail).WithPublicDetail(detail)
}

func paginationContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return xerror.Timeout(xerror.DataSource).With(err).WithPublicDetail("HTTP pagination was canceled or exceeded its deadline; no partial result was returned")
	}
	return nil
}

func (c *Connector) executePaginated(ctx context.Context, data json.RawMessage, definition httpQueryData, params map[string]types.TypedParam, limits types.ExecutionLimits) (*result.ExecutionResult, error) {
	start := time.Now()
	fail := func(err error) (*result.ExecutionResult, error) {
		return result.NewHTTPResult(nil, time.Since(start).Milliseconds(), err), nil
	}
	p := definition.Pagination
	pageNumber := p.PageParam != "" || p.PageSizeParam != "" || p.PageSize != 0 || p.TotalPagesPath != "" || p.PageNumberPath != ""
	if definition.Method != http.MethodGet || p.MaxPages < 1 || p.MaxPages > 1000 || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 300 {
		return fail(invalidPagination("HTTP pagination requires GET, max_pages (1-1000), and timeout_seconds (1-300)"))
	}
	terminalPath := p.NextCursorPath
	nextLink := p.NextLinkPath != ""
	switch {
	case nextLink:
		if pageNumber || p.CursorParam != "" || p.NextCursorPath != "" {
			return fail(invalidPagination("next-link pagination cannot be combined with cursor or page options"))
		}
		terminalPath = p.NextLinkPath
	case pageNumber:
		if p.CursorParam != "" || p.NextCursorPath != "" || p.PageParam == "" || p.PageSizeParam == "" || p.PageParam == p.PageSizeParam || p.PageSize < 1 || p.PageSize > 10000 || p.TotalPagesPath == "" || p.PageNumberPath == "" {
			return fail(invalidPagination("page pagination requires distinct page_param and page_size_param, page_size (1-10000), page_number_path, total_pages_path, and no cursor options"))
		}
		terminalPath = p.TotalPagesPath
	case p.CursorParam == "" || p.NextCursorPath == "":
		return fail(invalidPagination("cursor pagination requires cursor_param and next_cursor_path"))
	}
	paths := []string{p.RecordsPath, terminalPath}
	if pageNumber {
		paths = append(paths, p.PageNumberPath)
	}
	for _, path := range paths {
		if strings.ContainsAny(path, "[]*") {
			return fail(invalidPagination("HTTP pagination paths cannot use array indexes or wildcards"))
		}
		for _, part := range strings.Split(path, ".") {
			if part == "" {
				return fail(invalidPagination("HTTP pagination requires nonempty object-only dot paths"))
			}
		}
	}
	if p.RecordsPath == terminalPath || strings.HasPrefix(terminalPath, p.RecordsPath+".") || strings.HasPrefix(p.RecordsPath, terminalPath+".") {
		return fail(invalidPagination("HTTP pagination records and termination paths must not overlap"))
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	// Preserve the safety policy and forbid forwarding any authored credentials to another origin.
	paged := *c
	client := *c.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && !sameOrigin(req.URL, via[0].URL) {
			return invalidPagination("HTTP pagination redirects must remain on the original origin")
		}
		if c.client.CheckRedirect != nil {
			return c.client.CheckRedirect(req, via)
		}
		return nil
	}
	paged.client = &client
	var operation map[string]json.RawMessage
	if err := json.Unmarshal(data, &operation); err != nil {
		return fail(invalidPagination("invalid HTTP pagination operation"))
	}
	delete(operation, "pagination")
	if pageNumber {
		if definition.QueryParams == nil {
			definition.QueryParams = make(map[string]any)
		}
		definition.QueryParams[p.PageSizeParam] = p.PageSize
		queryParams, err := json.Marshal(definition.QueryParams)
		if err != nil {
			return fail(err)
		}
		operation["query_params"] = queryParams
	}
	normalized, err := normalizeParams(params)
	if err != nil {
		return fail(err)
	}
	query, err := param.SubstituteInMap(definition.QueryParams, normalized)
	if err != nil {
		return fail(err)
	}
	seen := map[string]bool{}
	if seed := query[p.CursorParam]; !pageNumber && !nextLink && seed != nil && fmt.Sprint(seed) != "" {
		seen[fmt.Sprint(seed)] = true
	}
	maxBytes := effectiveHTTPMaxResponseBytes(limits)
	if limits.MaxResponseBytes > 0 && limits.MaxResponseBytes < maxBytes {
		maxBytes = limits.MaxResponseBytes
	}
	maxRows := limits.MaxResultRows
	if maxRows <= 0 {
		maxRows = 1000
	}
	requestData, err := json.Marshal(operation)
	if err != nil {
		return fail(err)
	}
	var usedBytes int64
	records := make([]json.RawMessage, 0)
	var next *paginationCursor
	if nextLink {
		next = &paginationCursor{}
	}
	var origin *url.URL
	totalPages := -1
	for page := 0; page < p.MaxPages; page++ {
		if err := paginationContextError(ctx); err != nil {
			return fail(err)
		}
		pageLimits := limits
		pageLimits.HTTPMaxResponseBytes = maxBytes - usedBytes
		if pageLimits.HTTPMaxResponseBytes <= 0 {
			return fail(xerror.ExecutionResponseSizeExceeded())
		}
		if pageNumber {
			next = &paginationCursor{name: p.PageParam, value: strconv.Itoa(page + 1)}
		}
		execution, err := paged.executeSingle(ctx, requestData, params, next, pageLimits)
		if err != nil {
			return fail(err)
		}
		if err := paginationContextError(ctx); err != nil {
			return fail(err)
		}
		if execution.Error() != nil {
			return fail(execution.Error())
		}
		response, ok := execution.HTTP()
		if !ok || response == nil || response.File != nil {
			return fail(invalidPagination("HTTP pagination requires a successful JSON object response; no partial result was returned"))
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fail(invalidPagination(fmt.Sprintf("HTTP pagination request failed with status %d; no partial result was returned", response.StatusCode)))
		}
		if nextLink {
			current, err := url.Parse(next.targetURL)
			if err != nil {
				return fail(invalidPagination("invalid HTTP pagination response URL"))
			}
			if origin == nil {
				origin = current
			}
			if seen[current.String()] {
				return fail(invalidPagination("HTTP pagination did not progress: repeated response URL"))
			}
			seen[current.String()] = true
		}
		usedBytes += int64(len(response.RawBody))
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(response.Body, &envelope); err != nil || envelope == nil {
			return fail(invalidPagination("HTTP pagination requires a JSON object envelope"))
		}
		rawRecords, ok, pathErr := paginationPath(ctx, envelope, p.RecordsPath)
		var batch []json.RawMessage
		if pathErr != nil {
			return fail(pathErr)
		}
		if !ok || len(rawRecords) == 0 || rawRecords[0] != '[' || json.Unmarshal(rawRecords, &batch) != nil {
			return fail(invalidPagination("HTTP pagination records_path must select an array"))
		}
		if len(batch) > maxRows-len(records) {
			return fail(xerror.ExecutionRowLimitExceeded())
		}
		records = append(records, batch...)
		cursor := ""
		if pageNumber {
			rawTotal, exists, pathErr := paginationPath(ctx, envelope, p.TotalPagesPath)
			if pathErr != nil {
				return fail(pathErr)
			}
			var total int
			if !exists || string(rawTotal) == "null" || json.Unmarshal(rawTotal, &total) != nil || total < 0 {
				return fail(invalidPagination("HTTP pagination total_pages_path must select a nonnegative integer"))
			}
			if totalPages >= 0 && total != totalPages {
				return fail(invalidPagination("HTTP pagination total page count changed; no partial result was returned"))
			}
			totalPages = total
			if (total == 0 && (page != 0 || len(batch) != 0)) || (total > 0 && len(batch) == 0) || len(batch) > p.PageSize {
				return fail(invalidPagination("HTTP pagination page size or total page count is inconsistent"))
			}
			rawPage, exists, pathErr := paginationPath(ctx, envelope, p.PageNumberPath)
			if pathErr != nil {
				return fail(pathErr)
			}
			var responsePage int
			if !exists || string(rawPage) == "null" || json.Unmarshal(rawPage, &responsePage) != nil || responsePage != page+1 {
				return fail(invalidPagination("HTTP pagination did not progress: response page number must equal the requested page"))
			}
			if page+1 < total {
				cursor = strconv.Itoa(page + 2)
			}
		} else {
			rawCursor, exists, pathErr := paginationPath(ctx, envelope, terminalPath)
			if pathErr != nil {
				return fail(pathErr)
			}
			if exists && string(rawCursor) != "null" {
				if err := json.Unmarshal(rawCursor, &cursor); err != nil {
					return fail(invalidPagination("HTTP pagination next cursor or link must be a string or null"))
				}
			}
			if nextLink && cursor != "" {
				base, _ := url.Parse(next.targetURL)
				link, err := url.Parse(cursor)
				if err != nil || link.User != nil || link.Fragment != "" || link.Opaque != "" {
					return fail(invalidPagination("HTTP pagination next link must be a URL without user information or fragment"))
				}
				resolved := base.ResolveReference(link)
				if !sameOrigin(origin, resolved) {
					return fail(invalidPagination("HTTP pagination next links must remain on the original origin"))
				}
				cursor = resolved.String()
			}
		}
		if cursor == "" {
			combined, err := json.Marshal(records)
			if err != nil {
				return fail(err)
			}
			if err := setPaginationPath(ctx, envelope, strings.Split(p.RecordsPath, "."), combined); err != nil {
				return fail(err)
			}
			response.Body, err = json.Marshal(envelope)
			if err != nil {
				return fail(err)
			}
			if int64(len(response.Body)) > maxBytes {
				return fail(xerror.ExecutionResponseSizeExceeded())
			}
			if err := paginationContextError(ctx); err != nil {
				return fail(err)
			}
			response.RawBody = append([]byte(nil), response.Body...)
			count := len(records)
			response.PaginationRecordCount = &count
			return result.NewHTTPResult(response, time.Since(start).Milliseconds(), nil), nil
		}
		if seen[cursor] || len(batch) == 0 {
			return fail(invalidPagination("HTTP pagination did not progress: repeated cursor or empty continuing page"))
		}
		if nextLink {
			next = &paginationCursor{targetURL: cursor}
			continue
		}
		seen[cursor] = true
		// Cursors are opaque values, never URLs or templates.
		next = &paginationCursor{name: p.CursorParam, value: cursor}
	}
	return fail(invalidPagination("HTTP pagination page limit exceeded; no partial result was returned"))
}

func paginationPath(ctx context.Context, object map[string]json.RawMessage, path string) (json.RawMessage, bool, error) {
	parts := strings.Split(path, ".")
	for i, part := range parts {
		if err := paginationContextError(ctx); err != nil {
			return nil, false, err
		}
		value, ok := object[part]
		if !ok {
			return nil, false, nil
		}
		if i == len(parts)-1 {
			return value, true, nil
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(value, &nested) != nil || nested == nil {
			return nil, false, invalidPagination("HTTP pagination paths must traverse objects")
		}
		object = nested
	}
	return nil, false, nil
}

func setPaginationPath(ctx context.Context, object map[string]json.RawMessage, path []string, value json.RawMessage) error {
	if err := paginationContextError(ctx); err != nil {
		return err
	}
	if len(path) == 0 {
		return invalidPagination("HTTP pagination record path must not be empty")
	}
	if len(path) == 1 {
		object[path[0]] = value
		return nil
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(object[path[0]], &nested); err != nil {
		return err
	}
	// Release the old subtree instead of retaining a full copy at every depth.
	delete(object, path[0])
	if err := setPaginationPath(ctx, nested, path[1:], value); err != nil {
		return err
	}
	if err := paginationContextError(ctx); err != nil {
		return err
	}
	encoded, err := json.Marshal(nested)
	if err != nil {
		return err
	}
	object[path[0]] = encoded
	return nil
}
