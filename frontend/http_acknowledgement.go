package frontend

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/gofiber/fiber/v3"
)

func httpAcknowledgementFormFromRequest(c fiber.Ctx) views.HTTPAcknowledgementFormData {
	form := views.HTTPAcknowledgementFormData{
		Enabled:     c.FormValue("http_acknowledgement_enabled") == "on",
		StatusCodes: c.FormValue("http_acknowledgement_status_codes"),
	}
	paths := c.Request().PostArgs().PeekMulti("http_acknowledgement_path")
	values := c.Request().PostArgs().PeekMulti("http_acknowledgement_equals")
	for i := range max(len(paths), len(values)) {
		row := views.QueryKeyValueFormData{}
		if i < len(paths) {
			row.Key = string(paths[i])
		}
		if i < len(values) {
			row.Value = string(values[i])
		}
		form.Success = append(form.Success, row)
	}
	for _, name := range c.Request().PostArgs().PeekMulti("http_acknowledgement_empty_field") {
		form.EmptyFields = append(form.EmptyFields, string(name))
	}
	return form
}

func httpAcknowledgementFromForm(form views.HTTPAcknowledgementFormData) (*api.HTTPAcknowledgement, string) {
	if !form.Enabled {
		return nil, ""
	}
	contract := &api.HTTPAcknowledgement{}
	statuses := strings.Split(form.StatusCodes, ",")
	if len(statuses) > 16 {
		return nil, "Acknowledgement: enter 1–16 distinct HTTP status codes from 200 to 299, separated by commas."
	}
	seenStatuses := map[int]bool{}
	for _, text := range statuses {
		status, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || status < 200 || status > 299 || seenStatuses[status] {
			return nil, "Acknowledgement: enter 1–16 distinct HTTP status codes from 200 to 299, separated by commas."
		}
		seenStatuses[status] = true
		contract.StatusCodes = append(contract.StatusCodes, status)
	}
	if len(form.Success) < 1 || len(form.Success) > 8 {
		return nil, "Acknowledgement: add 1–8 success checks."
	}
	for i, row := range form.Success {
		parts := strings.Split(row.Key, ".")
		if row.Key == "" || len(row.Key) > 256 || len(parts) > 16 || strings.ContainsAny(row.Key, "[]*{}") {
			return nil, fmt.Sprintf("Acknowledgement success check %d: enter an object path with up to 16 dot-separated segments and 256 bytes; arrays, wildcards and templates are unsupported.", i+1)
		}
		for _, part := range parts {
			if part == "" {
				return nil, fmt.Sprintf("Acknowledgement success check %d: path segments must not be empty.", i+1)
			}
		}
		value := strings.TrimSpace(row.Value)
		// Keep the raw scalar: decoding and re-encoding numbers would change acknowledgement semantics.
		if !json.Valid([]byte(value)) || value == "null" || value[0] == '[' || value[0] == '{' {
			return nil, fmt.Sprintf("Acknowledgement success check %d: enter a JSON string (in double quotes), boolean or number; null, objects and arrays are unsupported.", i+1)
		}
		contract.Success = append(contract.Success, api.HTTPAcknowledgementCheck{Path: row.Key, Equals: json.RawMessage(value)})
	}
	if len(form.EmptyFields) > 8 {
		return nil, "Acknowledgement: use at most 8 distinct empty field names, each 1–64 bytes."
	}
	seenFields := map[string]bool{}
	for i, name := range form.EmptyFields {
		if len(name) == 0 || len(name) > 64 || seenFields[name] {
			return nil, fmt.Sprintf("Acknowledgement empty field %d: enter a distinct field name of 1–64 bytes, or remove the row.", i+1)
		}
		seenFields[name] = true
	}
	if len(form.EmptyFields) > 0 {
		contract.EmptyFields = &form.EmptyFields
	}
	return contract, ""
}
