package frontend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

func TestHTTPAcknowledgementFormRoundTrip(t *testing.T) {
	t.Parallel()
	const statementJSON = `{"type":"http","method":"POST","path":"/records","headers":{"X-Custom":"kept"},"query_params":{"mode":"all"},"body":{"records":[]},"write_retry":{"verified":true,"provider_contract_url":"https://provider.invalid/idempotency","key_header":"Idempotency-Key","retention_seconds":3600,"payload_conflict":"reject"},"acknowledgement":{"status_codes":[200,201],"success":[{"path":"status.code","equals":"OK"},{"path":"valid","equals":true},{"path":"zero","equals":false},{"path":"decimal","equals":1.0},{"path":"exponent","equals":1e+02},{"path":"large","equals":9007199254740993},{"path":"negative","equals":-0},{"path":"empty","equals":""}],"empty_fields":["errors","validation,errors"]}}`
	var statement api.QueryStatement
	require.NoError(t, json.Unmarshal([]byte(statementJSON), &statement))
	form := views.QueryEditorFormDataFromQuery(api.Query{Name: "Write records", QueryStatement: statement})
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "SaveAndReopen", false: "Remove"}[enabled], func(t *testing.T) {
			values := url.Values{
				"name": {"Write records"}, "http_method": {form.HTTPMethod}, "http_path": {form.HTTPPath},
				"http_body": {form.HTTPBody}, "http_write_retry": {form.HTTPWriteRetry},
				"http_header_key": {"X-Custom"}, "http_header_value": {"kept"},
				"http_acknowledgement_status_codes": {form.HTTPAcknowledgement.StatusCodes},
				"http_acknowledgement_empty_field":  form.HTTPAcknowledgement.EmptyFields,
			}
			if enabled {
				values.Set("http_acknowledgement_enabled", "on")
			}
			for _, check := range form.HTTPAcknowledgement.Success {
				values.Add("http_acknowledgement_path", check.Key)
				values.Add("http_acknowledgement_equals", check.Value)
			}
			app := fiber.New()
			app.Post("/", func(c fiber.Ctx) error {
				payload, message := buildCreateQueryPayload(c, api.DataSourceTypeHttp)
				require.Empty(t, message)
				got, err := payload.QueryStatement.AsHTTPStatement()
				require.NoError(t, err)
				if enabled {
					raw, err := payload.QueryStatement.MarshalJSON()
					require.NoError(t, err)
					require.JSONEq(t, statementJSON, string(raw))
					reopened := views.QueryEditorFormDataFromQuery(api.Query{QueryStatement: payload.QueryStatement})
					require.Equal(t, form.HTTPAcknowledgement, reopened.HTTPAcknowledgement)
				} else {
					require.Nil(t, got.Acknowledgement)
				}
				require.Equal(t, "/records", got.Path)
				require.Equal(t, "all", (*got.QueryParams)["mode"])
				require.Equal(t, "kept", (*got.Headers)["X-Custom"])
				require.NotNil(t, got.WriteRetry)
				require.NotNil(t, got.Body)
				return c.SendStatus(http.StatusOK)
			})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(values.Encode()))
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			response, err := app.Test(req)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusOK, response.StatusCode)
		})
	}
}

func TestHTTPAcknowledgementFormValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		change  func(*views.HTTPAcknowledgementFormData)
		message string
	}{
		{"MissingStatuses", func(f *views.HTTPAcknowledgementFormData) { f.StatusCodes = "" }, "HTTP status codes"},
		{"DuplicateStatus", func(f *views.HTTPAcknowledgementFormData) { f.StatusCodes = "200,200" }, "distinct HTTP status codes"},
		{"Non2xxStatus", func(f *views.HTTPAcknowledgementFormData) { f.StatusCodes = "400" }, "200 to 299"},
		{"MalformedStatus", func(f *views.HTTPAcknowledgementFormData) { f.StatusCodes = "200," }, "HTTP status codes"},
		{"TooManyStatuses", func(f *views.HTTPAcknowledgementFormData) { f.StatusCodes = strings.Repeat("200,", 16) + "201" }, "1–16"},
		{"NoChecks", func(f *views.HTTPAcknowledgementFormData) { f.Success = nil }, "1–8 success checks"},
		{"TooManyChecks", func(f *views.HTTPAcknowledgementFormData) { f.Success = make([]views.QueryKeyValueFormData, 9) }, "1–8 success checks"},
		{"EmptyPath", func(f *views.HTTPAcknowledgementFormData) { f.Success[0].Key = "" }, "success check 1"},
		{"ArrayPath", func(f *views.HTTPAcknowledgementFormData) { f.Success[0].Key = "items[0].code" }, "object path"},
		{"EmptySegment", func(f *views.HTTPAcknowledgementFormData) { f.Success[0].Key = "status..code" }, "segments must not be empty"},
		{"DeepPath", func(f *views.HTTPAcknowledgementFormData) { f.Success[0].Key = strings.Repeat("a.", 16) + "a" }, "16 dot-separated segments"},
		{"LongPath", func(f *views.HTTPAcknowledgementFormData) { f.Success[0].Key = strings.Repeat("a", 257) }, "256 bytes"},
		{"MissingValue", func(f *views.HTTPAcknowledgementFormData) { f.Success[0].Value = "" }, "JSON string"},
		{"BareString", func(f *views.HTTPAcknowledgementFormData) { f.Success[0].Value = "OK" }, "double quotes"},
		{"NullValue", func(f *views.HTTPAcknowledgementFormData) { f.Success[0].Value = "null" }, "null, objects and arrays"},
		{"ObjectValue", func(f *views.HTTPAcknowledgementFormData) { f.Success[0].Value = `{}` }, "null, objects and arrays"},
		{"ArrayValue", func(f *views.HTTPAcknowledgementFormData) { f.Success[0].Value = `[]` }, "null, objects and arrays"},
		{"TrailingJSON", func(f *views.HTTPAcknowledgementFormData) { f.Success[0].Value = `true false` }, "JSON string"},
		{"DuplicateEmptyField", func(f *views.HTTPAcknowledgementFormData) { f.EmptyFields = []string{"errors", "errors"} }, "distinct field name"},
		{"BlankEmptyField", func(f *views.HTTPAcknowledgementFormData) { f.EmptyFields = []string{""} }, "remove the row"},
		{"LongEmptyField", func(f *views.HTTPAcknowledgementFormData) { f.EmptyFields = []string{strings.Repeat("é", 33)} }, "1–64 bytes"},
		{"TooManyEmptyFields", func(f *views.HTTPAcknowledgementFormData) { f.EmptyFields = make([]string, 9) }, "at most 8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := views.HTTPAcknowledgementFormData{Enabled: true, StatusCodes: "200", Success: []views.QueryKeyValueFormData{{Key: "status.code", Value: `"OK"`}}}
			tc.change(&form)
			contract, message := httpAcknowledgementFromForm(form)
			require.Nil(t, contract)
			require.Contains(t, message, tc.message)
			form.Enabled = false
			contract, message = httpAcknowledgementFromForm(form)
			require.Nil(t, contract)
			require.Empty(t, message)
		})
	}
}
