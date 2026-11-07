package main

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	validToken              = "pat-na1-test-token"
	localOAuthClientID      = "braidplane-local-oauth-client"
	localOAuthClientSecret  = "braidplane-local-oauth-secret"
	localOAuthCode          = "hubspot-local-authorization-code"
	localOAuthRefreshToken  = "hubspot-local-refresh-token"
	hubSpotOAuthCallbackURL = "/provider-connectors/hubspot/callback"
)

var oauthScopes = []string{
	"oauth",
	"crm.objects.contacts.read",
	"crm.objects.companies.read",
	"crm.objects.deals.read",
	"crm.objects.custom.read",
	"crm.objects.contacts.write",
	"crm.objects.companies.write",
	"crm.objects.deals.write",
	"crm.objects.custom.write",
}

var customObjectTypeIDPattern = regexp.MustCompile(`^2-[1-9][0-9]*$`)

var defaultProperties = map[string][]property{
	"contacts": {
		{Name: "email", Label: "Email", Type: "string", FormField: true},
		{Name: "firstname", Label: "First name", Type: "string", FormField: false},
		{Name: "lastname", Label: "Last name", Type: "string", FormField: false},
	},
	"companies": {
		{Name: "name", Label: "Company name", Type: "string", FormField: true},
		{Name: "domain", Label: "Domain", Type: "string", FormField: false},
	},
	"deals": {
		{Name: "dealname", Label: "Deal name", Type: "string", FormField: true},
		{Name: "amount", Label: "Amount", Type: "number", FormField: false},
	},
}

type property struct {
	Name           string `json:"name"`
	Label          string `json:"label"`
	Type           string `json:"type"`
	FormField      bool   `json:"formField"`
	HasUniqueValue bool   `json:"hasUniqueValue"`
	ReadOnlyValue  bool   `json:"readOnlyValue"`
}

type objectRecord struct {
	ID         string         `json:"id"`
	Properties map[string]any `json:"properties"`
}

type seedRequest struct {
	Properties    map[string][]property `json:"properties"`
	Contacts      []objectRecord        `json:"contacts"`
	Companies     []objectRecord        `json:"companies"`
	Deals         []objectRecord        `json:"deals"`
	CustomObjects []customObjectSeed    `json:"custom_objects"`
}

type customObjectSeed struct {
	ObjectTypeID string         `json:"object_type_id"`
	Name         string         `json:"name"`
	Label        string         `json:"label"`
	Properties   []property     `json:"properties"`
	Records      []objectRecord `json:"records"`
}

type searchRequest struct {
	Query        string        `json:"query"`
	Properties   []string      `json:"properties"`
	Sorts        []string      `json:"sorts"`
	After        string        `json:"after"`
	Limit        int           `json:"limit"`
	FilterGroups []filterGroup `json:"filterGroups"`
}

type batchUpsertRequest struct {
	Inputs []batchUpsertInput `json:"inputs"`
}

type batchUpsertInput struct {
	ID         string         `json:"id"`
	IDProperty string         `json:"idProperty"`
	Properties map[string]any `json:"properties"`
}

type filterGroup struct {
	Filters []filter `json:"filters"`
}

type filter struct {
	PropertyName string `json:"propertyName"`
	Operator     string `json:"operator"`
	Value        any    `json:"value"`
	HighValue    any    `json:"highValue"`
	Values       []any  `json:"values"`
}

type store struct {
	mu            sync.Mutex
	nextID        int
	properties    map[string][]property
	objects       map[string][]objectRecord
	customObjects map[string]customObjectSeed
}

func newStore() *store {
	s := &store{}
	s.reset()
	return s
}

func (s *store) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID = 100
	s.properties = cloneProperties(defaultProperties)
	s.objects = map[string][]objectRecord{
		"contacts":  {},
		"companies": {},
		"deals":     {},
	}
	s.customObjects = map[string]customObjectSeed{}
}

func (s *store) seed(req seedRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, customObject := range req.CustomObjects {
		objectTypeID := strings.TrimSpace(customObject.ObjectTypeID)
		if !isCustomObjectTypeID(objectTypeID) {
			continue
		}
		customObject.ObjectTypeID = objectTypeID
		customObject.Name = strings.TrimSpace(customObject.Name)
		customObject.Label = strings.TrimSpace(customObject.Label)
		customObject.Properties = clonePropertySlice(customObject.Properties)
		s.customObjects[objectTypeID] = customObject
		s.properties[objectTypeID] = clonePropertySlice(customObject.Properties)
		s.objects[objectTypeID] = appendSeededRecords(
			s.objects[objectTypeID],
			customObject.Records,
			&s.nextID,
		)
	}

	for objectType, props := range req.Properties {
		if !s.supportsObjectTypeLocked(objectType) {
			continue
		}
		s.properties[objectType] = clonePropertySlice(props)
	}

	s.objects["contacts"] = appendSeededRecords(s.objects["contacts"], req.Contacts, &s.nextID)
	s.objects["companies"] = appendSeededRecords(s.objects["companies"], req.Companies, &s.nextID)
	s.objects["deals"] = appendSeededRecords(s.objects["deals"], req.Deals, &s.nextID)
}

func (s *store) supportsObjectType(objectType string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.supportsObjectTypeLocked(objectType)
}

func (s *store) supportsObjectTypeLocked(objectType string) bool {
	_, ok := s.objects[objectType]
	return ok
}

func (s *store) customObjectSchemas() []customObjectSeed {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := make([]string, 0, len(s.customObjects))
	for objectTypeID := range s.customObjects {
		keys = append(keys, objectTypeID)
	}
	sort.Strings(keys)
	result := make([]customObjectSeed, 0, len(keys))
	for _, objectTypeID := range keys {
		item := s.customObjects[objectTypeID]
		item.Properties = clonePropertySlice(item.Properties)
		item.Records = nil
		result = append(result, item)
	}
	return result
}

func (s *store) propertiesFor(objectType string) ([]property, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	props, ok := s.properties[objectType]
	if !ok {
		return nil, false
	}

	return clonePropertySlice(props), true
}

func (s *store) list(
	objectType string,
	properties []string,
	after string,
	limit int,
) ([]objectRecord, string, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, ok := s.objects[objectType]
	if !ok {
		return nil, "", false, false
	}

	start := parseAfter(after)
	if start > len(records) {
		start = len(records)
	}
	end := start + normalizeLimit(limit)
	if end > len(records) {
		end = len(records)
	}

	page := projectRecords(records[start:end], properties)
	nextAfter, hasMore := nextPageCursor(end, len(records))
	return page, nextAfter, hasMore, true
}

func (s *store) search(
	objectType string,
	req searchRequest,
) ([]objectRecord, string, bool, int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, ok := s.objects[objectType]
	if !ok {
		return nil, "", false, 0, false, nil
	}

	matched := make([]objectRecord, 0, len(records))
	for _, record := range records {
		if !matchesQuery(record, req.Query) {
			continue
		}
		ok, err := matchesFilterGroups(record, req.FilterGroups)
		if err != nil {
			return nil, "", false, 0, true, err
		}
		if !ok {
			continue
		}
		matched = append(matched, cloneRecord(record))
	}

	applySorts(matched, req.Sorts)

	start := parseAfter(req.After)
	if start > len(matched) {
		start = len(matched)
	}
	end := start + normalizeLimit(req.Limit)
	if end > len(matched) {
		end = len(matched)
	}

	page := projectRecords(matched[start:end], req.Properties)
	nextAfter, hasMore := nextPageCursor(end, len(matched))
	return page, nextAfter, hasMore, len(matched), true, nil
}

func (s *store) upsert(
	objectType string,
	inputs []batchUpsertInput,
) ([]map[string]any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, ok := s.objects[objectType]
	if !ok {
		return nil, false, nil
	}
	propertyByName := make(map[string]property, len(s.properties[objectType]))
	for _, item := range s.properties[objectType] {
		propertyByName[item.Name] = item
	}
	results := make([]map[string]any, 0, len(inputs))
	// Validate the complete request before mutating the store so rejected batches are atomic.
	for _, input := range inputs {
		idValue := strings.TrimSpace(input.ID)
		idProperty := strings.TrimSpace(input.IDProperty)
		idSchema, exists := propertyByName[idProperty]
		if idValue == "" || !exists || (!idSchema.HasUniqueValue && !(objectType == "contacts" && idProperty == "email")) {
			return nil, true, fmt.Errorf("invalid unique id property")
		}
		for name := range input.Properties {
			propertySchema, exists := propertyByName[name]
			if !exists || propertySchema.ReadOnlyValue {
				return nil, true, fmt.Errorf("invalid writable property")
			}
		}
	}

	for _, input := range inputs {
		idValue := strings.TrimSpace(input.ID)
		idProperty := strings.TrimSpace(input.IDProperty)
		match := -1
		for index := range records {
			if fmt.Sprint(records[index].Properties[idProperty]) == idValue {
				match = index
				break
			}
		}
		created := match == -1
		if created {
			s.nextID++
			records = append(records, objectRecord{
				ID:         strconv.Itoa(s.nextID),
				Properties: map[string]any{idProperty: idValue},
			})
			match = len(records) - 1
		}
		for name, value := range input.Properties {
			records[match].Properties[name] = value
		}
		results = append(results, map[string]any{
			"id":  records[match].ID,
			"new": created,
		})
	}
	s.objects[objectType] = records
	return results, true, nil
}

func appendSeededRecords(
	dst []objectRecord,
	src []objectRecord,
	nextID *int,
) []objectRecord {
	for _, record := range src {
		cloned := cloneRecord(record)
		if strings.TrimSpace(cloned.ID) == "" {
			cloned.ID = strconv.Itoa(*nextID)
			*nextID++
		}
		if cloned.Properties == nil {
			cloned.Properties = map[string]any{}
		}
		dst = append(dst, cloned)
	}

	sort.Slice(dst, func(i, j int) bool {
		return compareValues(dst[i].ID, dst[j].ID) < 0
	})

	return dst
}

func projectRecords(records []objectRecord, properties []string) []objectRecord {
	projected := make([]objectRecord, 0, len(records))
	for _, record := range records {
		projectedRecord := objectRecord{
			ID:         record.ID,
			Properties: map[string]any{},
		}

		if len(properties) == 0 {
			for key, value := range record.Properties {
				projectedRecord.Properties[key] = value
			}
			projected = append(projected, projectedRecord)
			continue
		}

		for _, propertyName := range properties {
			if value, ok := record.Properties[propertyName]; ok {
				projectedRecord.Properties[propertyName] = value
			}
		}
		projected = append(projected, projectedRecord)
	}

	return projected
}

func nextPageCursor(end, total int) (string, bool) {
	if end >= total {
		return "", false
	}
	return strconv.Itoa(end), true
}

func parseAfter(after string) int {
	if strings.TrimSpace(after) == "" {
		return 0
	}
	value, err := strconv.Atoi(after)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	return limit
}

func cloneProperties(src map[string][]property) map[string][]property {
	cloned := make(map[string][]property, len(src))
	for key, props := range src {
		cloned[key] = clonePropertySlice(props)
	}
	return cloned
}

func clonePropertySlice(src []property) []property {
	cloned := make([]property, 0, len(src))
	cloned = append(cloned, src...)
	return cloned
}

func cloneRecord(record objectRecord) objectRecord {
	cloned := objectRecord{
		ID:         record.ID,
		Properties: map[string]any{},
	}
	for key, value := range record.Properties {
		cloned.Properties[key] = value
	}
	return cloned
}

func matchesQuery(record objectRecord, query string) bool {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(record.ID), query) {
		return true
	}
	for _, value := range record.Properties {
		if strings.Contains(strings.ToLower(toString(value)), query) {
			return true
		}
	}
	return false
}

func matchesFilterGroups(record objectRecord, groups []filterGroup) (bool, error) {
	if len(groups) == 0 {
		return true, nil
	}

	for _, group := range groups {
		allMatched := true
		for _, filter := range group.Filters {
			ok, err := matchesFilter(record, filter)
			if err != nil {
				return false, err
			}
			if !ok {
				allMatched = false
				break
			}
		}
		if allMatched {
			return true, nil
		}
	}

	return false, nil
}

func matchesFilter(record objectRecord, f filter) (bool, error) {
	value, ok := record.Properties[f.PropertyName]
	if !ok && f.PropertyName == "hs_object_id" {
		value = record.ID
		ok = true
	}
	if !ok {
		value = nil
	}

	switch strings.ToUpper(strings.TrimSpace(f.Operator)) {
	case "", "EQ":
		return compareValues(value, f.Value) == 0, nil
	case "NEQ":
		return compareValues(value, f.Value) != 0, nil
	case "IN":
		for _, candidate := range f.Values {
			if compareValues(value, candidate) == 0 {
				return true, nil
			}
		}
		return false, nil
	case "NOT_IN":
		for _, candidate := range f.Values {
			if compareValues(value, candidate) == 0 {
				return false, nil
			}
		}
		return true, nil
	case "CONTAINS_TOKEN":
		return strings.Contains(strings.ToLower(toString(value)), strings.ToLower(toString(f.Value))), nil
	case "GT":
		return compareValues(value, f.Value) > 0, nil
	case "GTE":
		return compareValues(value, f.Value) >= 0, nil
	case "LT":
		return compareValues(value, f.Value) < 0, nil
	case "LTE":
		return compareValues(value, f.Value) <= 0, nil
	case "BETWEEN":
		return compareValues(value, f.Value) >= 0 && compareValues(value, f.HighValue) <= 0, nil
	default:
		return false, fmt.Errorf("unsupported operator: %s", f.Operator)
	}
}

func applySorts(records []objectRecord, sorts []string) {
	if len(sorts) == 0 {
		sort.SliceStable(records, func(i, j int) bool {
			return compareValues(records[i].ID, records[j].ID) < 0
		})
		return
	}

	sort.SliceStable(records, func(i, j int) bool {
		for _, rawSort := range sorts {
			sortField := strings.TrimSpace(rawSort)
			desc := strings.HasPrefix(sortField, "-")
			sortField = strings.TrimPrefix(sortField, "-")

			left := sortableValue(records[i], sortField)
			right := sortableValue(records[j], sortField)
			cmp := compareValues(left, right)
			if cmp == 0 {
				continue
			}
			if desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return compareValues(records[i].ID, records[j].ID) < 0
	})
}

func sortableValue(record objectRecord, field string) any {
	if field == "hs_object_id" {
		return record.ID
	}
	return record.Properties[field]
}

func compareValues(left, right any) int {
	if lf, lok := asFloat(left); lok {
		if rf, rok := asFloat(right); rok {
			switch {
			case lf < rf:
				return -1
			case lf > rf:
				return 1
			default:
				return 0
			}
		}
	}

	ls := strings.ToLower(toString(left))
	rs := strings.ToLower(toString(right))
	switch {
	case ls < rs:
		return -1
	case ls > rs:
		return 1
	default:
		return 0
	}
}

func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case json.Number:
		f, err := typed.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func toString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(strings.Trim(strings.TrimSpace(mustJSON(value)), `"`)), "\n", " "))
	}
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func isCustomObjectTypeID(objectType string) bool {
	return customObjectTypeIDPattern.MatchString(strings.TrimSpace(objectType))
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeUnauthorized(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"status":  "error",
		"message": "Authentication credentials not found",
	})
}

func requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Authorization")) != "Bearer "+validToken {
		writeUnauthorized(w)
		return false
	}
	return true
}

// validOAuthRedirectURI restricts the test provider to the canonical callback on a loopback origin.
func validOAuthRedirectURI(raw string) bool {
	redirectURI, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || redirectURI.Scheme != "http" || redirectURI.Host == "" || redirectURI.Path != hubSpotOAuthCallbackURL {
		return false
	}

	switch strings.ToLower(redirectURI.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// oauthCallbackURL builds an allowlisted callback result without reflecting arbitrary provider parameters.
func oauthCallbackURL(redirectURI string, values url.Values) string {
	callback, _ := url.Parse(redirectURI)
	callback.RawQuery = values.Encode()
	return callback.String()
}

// handleOAuthAuthorize renders deterministic approval and denial links for browser acceptance tests.
func handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	query := r.URL.Query()
	redirectURI := query.Get("redirect_uri")
	state := strings.TrimSpace(query.Get("state"))
	if query.Get("client_id") != localOAuthClientID || !validOAuthRedirectURI(redirectURI) || state == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}

	approveURL := oauthCallbackURL(redirectURI, url.Values{
		"code":  {localOAuthCode},
		"state": {state},
	})
	denyURL := oauthCallbackURL(redirectURI, url.Values{
		"error": {"access_denied"},
		"state": {state},
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Mock HubSpot consent</title></head><body>` +
		`<main><h1>Connect Braidplane to Mock HubSpot</h1>` +
		`<p>Requested scopes: ` + html.EscapeString(query.Get("scope")+" "+query.Get("optional_scopes")) + `</p>` +
		`<a href="` + html.EscapeString(approveURL) + `">Approve access</a> ` +
		`<a href="` + html.EscapeString(denyURL) + `">Deny access</a></main></body></html>`))
}

// handleOAuthToken exchanges only the fixture's fixed code or refresh token for deterministic grant metadata.
func handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	if r.Form.Get("client_id") != localOAuthClientID || r.Form.Get("client_secret") != localOAuthClientSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})
		return
	}

	switch r.Form.Get("grant_type") {
	case "authorization_code":
		if r.Form.Get("code") != localOAuthCode || !validOAuthRedirectURI(r.Form.Get("redirect_uri")) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
			return
		}
	case "refresh_token":
		if r.Form.Get("refresh_token") != localOAuthRefreshToken {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported_grant_type"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  validToken,
		"refresh_token": localOAuthRefreshToken,
		"expires_in":    1800,
		"scopes":        oauthScopes,
		"hub_id":        12345,
	})
}

func main() {
	st := newStore()
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/oauth/authorize", handleOAuthAuthorize)
	mux.HandleFunc("/oauth/2026-03/token", handleOAuthToken)

	mux.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}

		st.reset()
		writeJSON(w, http.StatusOK, map[string]any{"reset": true})
	})

	mux.HandleFunc("/seed", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}

		var req seedRequest
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
			return
		}

		st.seed(req)
		writeJSON(w, http.StatusOK, map[string]any{"seeded": true})
	})

	mux.HandleFunc("/account-info/v3/details", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}
		if !requireAuth(w, r) {
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"portalId":    12345,
			"companyName": "Mock HubSpot Account",
		})
	})

	mux.HandleFunc("/crm/properties/2026-03/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}
		if !requireAuth(w, r) {
			return
		}

		objectType := strings.TrimPrefix(r.URL.Path, "/crm/properties/2026-03/")
		props, ok := st.propertiesFor(objectType)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "object type not found"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"results": props})
	})

	mux.HandleFunc("/crm-object-schemas/2026-03/schemas", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}
		if !requireAuth(w, r) {
			return
		}

		schemas := st.customObjectSchemas()
		results := make([]map[string]any, 0, len(schemas))
		for _, schema := range schemas {
			results = append(results, map[string]any{
				"archived":     false,
				"name":         schema.Name,
				"objectTypeId": schema.ObjectTypeID,
				"labels": map[string]any{
					"singular": schema.Label,
					"plural":   schema.Label,
				},
				"properties": schema.Properties,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	})

	mux.HandleFunc("/crm/objects/2026-03/", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/crm/objects/2026-03/")
		if strings.HasSuffix(path, "/batch/upsert") {
			if r.Method != http.MethodPost {
				writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
				return
			}
			objectType := strings.TrimSuffix(path, "/batch/upsert")
			var req batchUpsertRequest
			if err := decodeJSON(r, &req); err != nil || len(req.Inputs) == 0 || len(req.Inputs) > 100 {
				writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": "invalid upsert batch"})
				return
			}
			results, ok, err := st.upsert(objectType, req.Inputs)
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "object type not found"})
				return
			}
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"status": "error", "message": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "COMPLETE", "results": results})
			return
		}
		if strings.HasSuffix(path, "/search") {
			if r.Method != http.MethodPost {
				writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
				return
			}

			objectType := strings.TrimSuffix(path, "/search")
			if !st.supportsObjectType(objectType) {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "object type not found"})
				return
			}

			var req searchRequest
			if err := decodeJSON(r, &req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
				return
			}

			results, nextAfter, hasMore, total, ok, err := st.search(objectType, req)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"status":  "error",
					"message": err.Error(),
				})
				return
			}
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "object type not found"})
				return
			}

			payload := map[string]any{
				"total":   total,
				"results": results,
			}
			if hasMore {
				payload["paging"] = map[string]any{
					"next": map[string]any{"after": nextAfter},
				}
			}
			writeJSON(w, http.StatusOK, payload)
			return
		}

		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}

		objectType := strings.Trim(path, "/")
		if !st.supportsObjectType(objectType) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "object type not found"})
			return
		}

		properties := splitCSV(r.URL.Query().Get("properties"))
		results, nextAfter, hasMore, ok := st.list(
			objectType,
			properties,
			r.URL.Query().Get("after"),
			parseAfter(r.URL.Query().Get("limit")),
		)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "object type not found"})
			return
		}

		payload := map[string]any{"results": results}
		if hasMore {
			payload["paging"] = map[string]any{
				"next": map[string]any{"after": nextAfter},
			}
		}
		writeJSON(w, http.StatusOK, payload)
	})

	server := &http.Server{Addr: ":8080", Handler: mux}
	log.Println("hubspot-mock server listening on :8080")
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}
