package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	validAccessToken     = "fake-bigquery-access-token"
	projectID            = "analytics-prod"
	defaultQueryPageSize = 1
)

type field struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Mode string `json:"mode"`
}

type tableData struct {
	Schema []field
	Rows   []map[string]any
}

type store struct {
	mu     sync.Mutex
	tables map[string]map[string]tableData
	jobs   map[string]queryResponse
	nextID int
}

type queryRequest struct {
	Query                string           `json:"query"`
	Location             string           `json:"location"`
	MaximumBytesBilled   int64            `json:"maximumBytesBilled,string"`
	MaxResults           int              `json:"maxResults"`
	ParameterMode        string           `json:"parameterMode"`
	QueryParameters      []queryParameter `json:"queryParameters"`
	UseLegacySQL         bool             `json:"useLegacySql"`
	TimeoutMilliseconds  int64            `json:"timeoutMs"`
	CreateSession        bool             `json:"createSession"`
	DefaultDataset       any              `json:"defaultDataset"`
	ConnectionProperties []any            `json:"connectionProperties"`
}

type queryParameter struct {
	Name          string `json:"name"`
	ParameterType struct {
		Type string `json:"type"`
	} `json:"parameterType"`
	ParameterValue struct {
		Value any `json:"value"`
	} `json:"parameterValue"`
}

type queryResponse struct {
	JobComplete  bool `json:"jobComplete"`
	JobReference struct {
		ProjectID string `json:"projectId"`
		JobID     string `json:"jobId"`
		Location  string `json:"location"`
	} `json:"jobReference"`
	Schema struct {
		Fields []field `json:"fields"`
	} `json:"schema"`
	Rows      []tableRow `json:"rows"`
	TotalRows string     `json:"totalRows"`
	PageToken string     `json:"pageToken,omitempty"`
}

type tableRow struct {
	Fields []tableCell `json:"f"`
}

type tableCell struct {
	Value any `json:"v"`
}

type insertRequest struct {
	Rows []struct {
		InsertID string         `json:"insertId"`
		JSON     map[string]any `json:"json"`
	} `json:"rows"`
}

func main() {
	st := newStore()
	mux := http.NewServeMux()
	registerRoutes(mux, st)

	server := &http.Server{Addr: ":8080", Handler: mux}
	log.Println("bigquery-mock server listening on :8080")
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func registerRoutes(mux *http.ServeMux, st *store) {
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}
		st.reset()
		writeJSON(w, http.StatusOK, map[string]any{"reset": true})
	})
	mux.HandleFunc("/token", handleToken)
	mux.HandleFunc("/inserted", func(w http.ResponseWriter, r *http.Request) {
		datasetID := firstNonEmpty(r.URL.Query().Get("dataset"), "analytics")
		tableID := firstNonEmpty(r.URL.Query().Get("table"), "order_exports")
		rows, ok := st.rows(datasetID, tableID)
		if !ok {
			writeJSON(w, http.StatusNotFound, providerError("table not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"row_count": len(rows), "rows": rows})
	})
	mux.HandleFunc("/bigquery/v2/projects/", func(w http.ResponseWriter, r *http.Request) {
		handleBigQuery(w, r, st)
	})
}

func newStore() *store {
	st := &store{}
	st.reset()
	return st
}

func (s *store) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tables = map[string]map[string]tableData{
		"analytics": {
			"orders": {
				Schema: orderSchema(),
				Rows: []map[string]any{
					{
						"order_id":     "o-100",
						"customer_id":  "c-100",
						"region":       "emea",
						"total_amount": "123.45",
						"active":       true,
						"item_count":   "2",
						"metadata":     `{"source":"fixture","tier":"gold"}`,
					},
					{
						"order_id":     "o-101",
						"customer_id":  "c-101",
						"region":       "americas",
						"total_amount": "67.89",
						"active":       false,
						"item_count":   "1",
						"metadata":     `{"source":"fixture","tier":"silver"}`,
					},
					{
						"order_id":     "o-102",
						"customer_id":  "c-102",
						"region":       "emea",
						"total_amount": "250.00",
						"active":       true,
						"item_count":   "5",
						"metadata":     `{"source":"fixture","tier":"platinum"}`,
					},
				},
			},
			"order_exports": {
				Schema: orderSchema(),
				Rows:   []map[string]any{},
			},
		},
	}
	s.jobs = map[string]queryResponse{}
	s.nextID = 1
}

func (s *store) datasets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]string, 0, len(s.tables))
	for datasetID := range s.tables {
		result = append(result, datasetID)
	}
	sort.Strings(result)
	return result
}

func (s *store) tableNames(datasetID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	tables := s.tables[datasetID]
	if tables == nil {
		return nil
	}
	result := make([]string, 0, len(tables))
	for tableID := range tables {
		result = append(result, tableID)
	}
	sort.Strings(result)
	return result
}

func (s *store) table(datasetID, tableID string) (tableData, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tables := s.tables[datasetID]
	if tables == nil {
		return tableData{}, false
	}
	table, ok := tables[tableID]
	if !ok {
		return tableData{}, false
	}
	return tableData{Schema: cloneSchema(table.Schema), Rows: cloneRows(table.Rows)}, true
}

func (s *store) rows(datasetID, tableID string) ([]map[string]any, bool) {
	table, ok := s.table(datasetID, tableID)
	if !ok {
		return nil, false
	}
	return table.Rows, true
}

func (s *store) insertRows(datasetID, tableID string, rows []map[string]any) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tables := s.tables[datasetID]
	if tables == nil {
		return 0, false
	}
	table, ok := tables[tableID]
	if !ok {
		return 0, false
	}
	table.Rows = append(table.Rows, cloneRows(rows)...)
	tables[tableID] = table
	return len(rows), true
}

func (s *store) recordJob(response queryResponse) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobID := fmt.Sprintf("job-%d", s.nextID)
	s.nextID++
	response.JobReference.JobID = jobID
	s.jobs[jobID] = response
	return jobID
}

func (s *store) job(jobID string) (queryResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	response, ok := s.jobs[jobID]
	return response, ok
}

func handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" ||
		strings.TrimSpace(r.Form.Get("assertion")) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": validAccessToken,
		"token_type":   "Bearer",
		"expires_in":   3600,
	})
}

func handleBigQuery(w http.ResponseWriter, r *http.Request, st *store) {
	if !requireAuth(w, r) {
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 5 || parts[0] != "bigquery" || parts[1] != "v2" || parts[2] != "projects" {
		writeJSON(w, http.StatusNotFound, providerError("not found"))
		return
	}
	if parts[3] != projectID {
		writeJSON(w, http.StatusForbidden, providerError("project not available in mock"))
		return
	}

	switch {
	case len(parts) == 5 && parts[4] == "datasets":
		handleListDatasets(w, r, st)
	case len(parts) == 6 && parts[4] == "datasets":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, providerError("method not allowed"))
			return
		}
		if !slices.Contains(st.datasets(), parts[5]) {
			writeJSON(w, http.StatusNotFound, providerError("dataset not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"datasetReference": map[string]any{"projectId": projectID, "datasetId": parts[5]}})
	case len(parts) == 7 && parts[4] == "datasets" && parts[6] == "tables":
		handleListTables(w, r, st, parts[5])
	case len(parts) == 8 && parts[4] == "datasets" && parts[6] == "tables":
		handleGetTable(w, r, st, parts[5], parts[7])
	case len(parts) == 9 && parts[4] == "datasets" && parts[6] == "tables" && parts[8] == "insertAll":
		handleInsertAll(w, r, st, parts[5], parts[7])
	case len(parts) == 5 && parts[4] == "queries":
		handleQuery(w, r, st)
	case len(parts) == 6 && parts[4] == "queries":
		handleQueryPage(w, r, st, parts[5])
	default:
		writeJSON(w, http.StatusNotFound, providerError("not found"))
	}
}

func handleListDatasets(w http.ResponseWriter, r *http.Request, st *store) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	items := []map[string]any{}
	for _, datasetID := range st.datasets() {
		items = append(items, map[string]any{
			"datasetReference": map[string]any{"projectId": projectID, "datasetId": datasetID},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"datasets": items})
}

func handleListTables(w http.ResponseWriter, r *http.Request, st *store, datasetID string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	names := st.tableNames(datasetID)
	if names == nil {
		writeJSON(w, http.StatusNotFound, providerError("dataset not found"))
		return
	}
	items := []map[string]any{}
	for _, tableID := range names {
		items = append(items, map[string]any{
			"tableReference": map[string]any{
				"projectId": projectID,
				"datasetId": datasetID,
				"tableId":   tableID,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tables": items})
}

func handleGetTable(w http.ResponseWriter, r *http.Request, st *store, datasetID, tableID string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	table, ok := st.table(datasetID, tableID)
	if !ok {
		writeJSON(w, http.StatusNotFound, providerError("table not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tableReference": map[string]any{
			"projectId": projectID,
			"datasetId": datasetID,
			"tableId":   tableID,
		},
		"type":   "TABLE",
		"schema": map[string]any{"fields": table.Schema},
	})
}

func handleQuery(w http.ResponseWriter, r *http.Request, st *store) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	var req queryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, providerError("invalid json"))
		return
	}

	response := buildQueryResponse(st, req)
	response.JobReference.ProjectID = projectID
	response.JobReference.Location = firstNonEmpty(req.Location, "US")
	jobID := st.recordJob(response)
	response.JobReference.JobID = jobID
	writeJSON(w, http.StatusOK, pagedQueryResponse(response, req.MaxResults, ""))
}

func handleQueryPage(w http.ResponseWriter, r *http.Request, st *store, jobID string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	response, ok := st.job(jobID)
	if !ok {
		writeJSON(w, http.StatusNotFound, providerError("job not found"))
		return
	}
	writeJSON(
		w,
		http.StatusOK,
		pagedQueryResponse(response, queryMaxResults(r), r.URL.Query().Get("pageToken")),
	)
}

func handleInsertAll(w http.ResponseWriter, r *http.Request, st *store, datasetID, tableID string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	var req insertRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, providerError("invalid json"))
		return
	}
	rows := make([]map[string]any, 0, len(req.Rows))
	for _, item := range req.Rows {
		rows = append(rows, item.JSON)
	}
	if _, ok := st.insertRows(datasetID, tableID, rows); !ok {
		writeJSON(w, http.StatusNotFound, providerError("table not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func buildQueryResponse(st *store, req queryRequest) queryResponse {
	datasetID, tableID := queryTarget(req.Query)
	table, ok := st.table(datasetID, tableID)
	if !ok {
		table = tableData{Schema: []field{{Name: "test_column", Type: "INT64", Mode: "NULLABLE"}}, Rows: []map[string]any{{"test_column": "1"}}}
	}

	rows := table.Rows
	if region := parameterValue(req.QueryParameters, "region"); region != "" {
		rows = filterRows(rows, "region", region)
	}

	response := queryResponse{JobComplete: true, TotalRows: strconv.Itoa(len(rows))}
	response.Schema.Fields = table.Schema
	response.Rows = tableRows(table.Schema, rows)
	return response
}

func pagedQueryResponse(full queryResponse, maxResults int, pageToken string) queryResponse {
	pageSize := maxResults
	if pageSize <= 0 {
		pageSize = defaultQueryPageSize
	}

	response := full
	response.PageToken = ""
	start := pageOffset(pageToken)
	if start > len(full.Rows) {
		start = len(full.Rows)
	}
	end := start + pageSize
	if end < len(full.Rows) {
		response.PageToken = strconv.Itoa(end)
	} else {
		end = len(full.Rows)
	}
	response.Rows = append([]tableRow(nil), full.Rows[start:end]...)
	return response
}

func queryMaxResults(r *http.Request) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("maxResults")))
	if err != nil || value < 1 {
		return 0
	}
	return value
}

func pageOffset(pageToken string) int {
	value, err := strconv.Atoi(strings.TrimSpace(pageToken))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func queryTarget(query string) (string, string) {
	switch {
	case strings.Contains(query, "`analytics.order_exports`"):
		return "analytics", "order_exports"
	default:
		return "analytics", "orders"
	}
}

func orderSchema() []field {
	return []field{
		{Name: "order_id", Type: "STRING", Mode: "REQUIRED"},
		{Name: "customer_id", Type: "STRING", Mode: "NULLABLE"},
		{Name: "region", Type: "STRING", Mode: "NULLABLE"},
		{Name: "total_amount", Type: "NUMERIC", Mode: "NULLABLE"},
		{Name: "active", Type: "BOOL", Mode: "NULLABLE"},
		{Name: "item_count", Type: "INT64", Mode: "NULLABLE"},
		{Name: "metadata", Type: "JSON", Mode: "NULLABLE"},
	}
}

func tableRows(schema []field, rows []map[string]any) []tableRow {
	result := make([]tableRow, 0, len(rows))
	for _, row := range rows {
		cells := make([]tableCell, 0, len(schema))
		for _, field := range schema {
			cells = append(cells, tableCell{Value: row[field.Name]})
		}
		result = append(result, tableRow{Fields: cells})
	}
	return result
}

func filterRows(rows []map[string]any, key, value string) []map[string]any {
	result := []map[string]any{}
	for _, row := range rows {
		if fmt.Sprint(row[key]) == value {
			result = append(result, row)
		}
	}
	return result
}

func parameterValue(params []queryParameter, name string) string {
	for _, param := range params {
		if param.Name == name {
			return fmt.Sprint(param.ParameterValue.Value)
		}
	}
	return ""
}

func requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") == "Bearer "+validAccessToken {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, providerError("invalid authentication credentials"))
	return false
}

func decodeJSON(r *http.Request, target any) error {
	defer func() { _ = r.Body.Close() }()
	return json.NewDecoder(io.LimitReader(r.Body, 10*1024*1024)).Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("write json response: %v", err)
	}
}

func providerError(message string) map[string]any {
	return map[string]any{"error": map[string]any{"message": message}}
}

func cloneSchema(schema []field) []field {
	return append([]field(nil), schema...)
}

func cloneRows(rows []map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		cloned := make(map[string]any, len(row))
		for key, value := range row {
			cloned[key] = value
		}
		result = append(result, cloned)
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
