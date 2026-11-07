package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadiness(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/readyz" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.Header().Set("Location", "http://untrusted.invalid")
				w.WriteHeader(status)
			}))
			defer server.Close()
			_, port, err := net.SplitHostPort(server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			// Ambient proxy settings must not intercept a local probe.
			t.Setenv("HTTP_PROXY", "http://untrusted.invalid")
			err = run([]string{port})
			if (err == nil) != (status == http.StatusOK) {
				t.Fatalf("status %d: %v", status, err)
			}
		})
	}
}

func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"0"}, {"65536"}, {"-1"}, {"http://example.test"}, {"3001", "extra"}} {
		if err := run(args); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
}

func TestUnavailableEndpoint(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	if err := run([]string{port}); err == nil {
		t.Fatal("unavailable endpoint reported ready")
	}
}
