package client

import (
	"context"
	"reflect"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"google.golang.org/api/bigquery/v2"
)

// QueryPages consumes bounded pages from one query job without retaining prior pages.
func (c *Client) QueryPages(ctx context.Context, req QueryRequest, consume func(*QueryResult) error) (retErr error) {
	if req.MaxResultRows <= 0 || consume == nil {
		return xerror.InvalidInput(xerror.Query).WithPublicDetail("BigQuery file queries require a positive row limit and page consumer.")
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	budget := effectiveMaxResponseBytes(req.MaxResponseBytes)
	dry := buildQueryPayload(req)
	dry.DryRun = true
	dry.MaxResults = 1
	checked, _, err := callSDK(ctx, 0, budget, func(ctx context.Context) (*bigquery.QueryResponse, error) {
		return c.api.Jobs.Query(req.ProjectID, dry).Context(ctx).Do()
	})
	if err != nil {
		return err
	}
	if checked.StatementType != "SELECT" || len(checked.Errors) > 0 {
		return xerror.InvalidInput(xerror.Query).WithPublicDetail("BigQuery file output requires one SELECT query; scripts and writes are not supported.")
	}
	payload := buildQueryPayload(req)
	payload.MaxResults = int64(min(999, req.MaxResultRows) + 1)
	response, body, err := callSDK(ctx, 0, budget, func(ctx context.Context) (*bigquery.QueryResponse, error) {
		return c.api.Jobs.Query(req.ProjectID, payload).Context(ctx).Do()
	})
	if err != nil {
		return err
	}
	jobID, location := "", req.Location
	if response.JobReference != nil {
		jobID = response.JobReference.JobId
		location = firstNonEmpty(response.JobReference.Location, location)
	}
	defer func() {
		if retErr == nil || jobID == "" {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _, _ = callSDK(cleanupCtx, 0, budget, func(ctx context.Context) (*bigquery.JobCancelResponse, error) {
			call := c.api.Jobs.Cancel(req.ProjectID, jobID)
			if location != "" {
				call.Location(location)
			}
			return call.Context(ctx).Do()
		})
	}()
	schema, err := fieldsFromResponse(body)
	if err != nil {
		return err
	}
	rows, token, complete := response.Rows, response.PageToken, response.JobComplete
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(response.Errors) > 0 {
			return xerror.SourceQueryInvalid().WithPublicDetail("BigQuery could not complete the SQL file query.")
		}
		if complete {
			if len(schema) == 0 {
				return xerror.Unavailable(xerror.Query).WithPublicDetail("BigQuery query results are missing their schema.")
			}
			page := &QueryResult{Schema: schema, Rows: []map[string]any{}}
			remaining := budget
			if err := appendProviderRows(page, rows, 0, &remaining); err != nil {
				return err
			}
			if len(page.Rows) > req.MaxResultRows-count {
				return xerror.ExecutionRowLimitExceeded()
			}
			if err := consume(page); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			count += len(page.Rows)
			if token == "" {
				return nil
			}
		}
		if jobID == "" {
			return xerror.Unavailable(xerror.Query).WithPublicDetail("BigQuery query results are missing their job identity.")
		}
		next, nextBody, err := callSDK(ctx, 0, budget, func(ctx context.Context) (*bigquery.GetQueryResultsResponse, error) {
			call := c.api.Jobs.GetQueryResults(req.ProjectID, jobID).MaxResults(int64(min(999, max(0, req.MaxResultRows-count)) + 1))
			if complete {
				call.PageToken(token)
			}
			if location != "" {
				call.Location(location)
			}
			return call.Context(ctx).Do()
		})
		if err != nil {
			return err
		}
		if len(next.Errors) > 0 {
			return xerror.SourceQueryInvalid().WithPublicDetail("BigQuery could not complete the SQL file query.")
		}
		fields, err := fieldsFromResponse(nextBody)
		if err != nil {
			return err
		}
		if len(fields) > 0 {
			if len(schema) > 0 && !reflect.DeepEqual(schema, fields) {
				return xerror.InvalidInput(xerror.Query).WithPublicDetail("BigQuery result schema changed during streaming.")
			}
			schema = fields
		}
		rows, token, complete = next.Rows, next.PageToken, next.JobComplete
	}
}
