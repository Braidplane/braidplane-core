package main

import (
	"io"
	"net/http"
)

var projectReadinessFixture bool

func handleProjectReadinessAPI(w http.ResponseWriter, r *http.Request) bool {
	var body string
	switch r.URL.Path {
	case prefix + "/readiness":
		body = `{"status":"blocked","environments":[{"environment_slug":"dev","status":"ready","diagnostics":[]},
			{"environment_slug":"staging","status":"blocked","diagnostics":[
				{"code":"missing_ref","pipeline_slug":"daily-sync","requirement_key":"archive"},
				{"code":"missing_binding","pipeline_slug":"daily-sync","requirement_key":"source"},
				{"code":"missing_variable","pipeline_slug":"daily-sync","requirement_key":"region"}]}]}`
	case prefix + "/pipelines/daily-sync/readiness":
		body = `{"status":"blocked","environments":[{"environment_slug":"staging","status":"blocked","requirements":[
			{"kind":"data_source_ref","key":"source","status":"blocked","usages":[{"step_slug":"fetch","field_path":"/steps/0/query/data_source_ref"}]}],
			"diagnostics":[{"code":"missing_binding","requirement_key":"source","field_path":"/steps/0/query/data_source_ref"}]}]}`
	case "/api/organizations/acme/runtime-variable-comparison":
		body = `{"environments":["staging"],"rows":[{"key":"region","used":false,"cells":[{"source":"missing","blocked":true}]}]}`
	case "/api/organizations/acme/environments":
		body = `{"environments":[{"slug":"dev","name":"Development"},{"slug":"staging","name":"Staging"}]}`
	default:
		return false
	}
	_, _ = io.WriteString(w, body)
	return true
}
