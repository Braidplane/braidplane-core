package sinkbinding

import "testing"

func TestRecordUpdatePathAndProjection(t *testing.T) {
	const target = "{{runtime.sink.record_id}}"
	for _, path := range []string{"/records/" + target, "api/v2/items/" + target, "/" + target} {
		for _, body := range []any{"{{input}}", map[string]any{"values": "{{input}}"}} {
			if !ValidRecordUpdateOperation(map[string]any{"type": "http", "method": "PUT", "path": path, "body": body}) {
				t.Errorf("valid bounded update rejected for %q", path)
			}
		}
	}
	for _, path := range []string{"/records/{{record_id}}", "https://other/records/" + target, "//other/" + target, "/a/../" + target, "/./" + target, "/a%2fb/" + target, "/a\\b/" + target, "/{{prefix}}/" + target, "/a?key=/" + target, "/a#f/" + target, "/a/" + target + "/suffix", "/a//" + target} {
		if ValidRecordUpdateOperation(map[string]any{"method": "PATCH", "path": path, "body": "{{input}}"}) {
			t.Errorf("unsafe path accepted: %q", path)
		}
	}
	for _, body := range []any{nil, []any{"{{input}}"}, map[string]any{"a": "{{input}}", "b": "more"}, map[string]any{"a": "{{input.secret}}"}, map[string]any{"{{input}}": "{{input}}"}} {
		if ValidRecordUpdateOperation(map[string]any{"method": "PATCH", "path": "/records/" + target, "body": body}) {
			t.Errorf("unsafe record projection accepted: %v", body)
		}
	}
}
