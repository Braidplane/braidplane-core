package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestHandleOAuthAuthorizeRestrictsCallbacks verifies the fixture cannot become an open redirect.
func TestHandleOAuthAuthorizeRestrictsCallbacks(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id="+localOAuthClientID+
		"&state=test-state&redirect_uri="+url.QueryEscape("https://attacker.example/provider-connectors/hubspot/callback"), nil)
	response := httptest.NewRecorder()

	handleOAuthAuthorize(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid external redirect to return 400, got %d", response.Code)
	}
}

// TestHandleOAuthAuthorizeRendersConsent verifies approval and denial preserve only the opaque state.
func TestHandleOAuthAuthorizeRendersConsent(t *testing.T) {
	t.Parallel()

	redirectURI := "http://localhost:3002/provider-connectors/hubspot/callback"
	request := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id="+localOAuthClientID+
		"&state=test-state&scope=oauth&optional_scopes=crm.objects.contacts.read&redirect_uri="+url.QueryEscape(redirectURI), nil)
	response := httptest.NewRecorder()

	handleOAuthAuthorize(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected consent page to return 200, got %d", response.Code)
	}
	body := response.Body.String()
	for _, expected := range []string{"Approve access", "Deny access", "state=test-state"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected consent page to contain %q, got %q", expected, body)
		}
	}
}

// TestHandleOAuthTokenExchangesFixtureCode verifies the mock issues the API-valid token and safe account metadata.
func TestHandleOAuthTokenExchangesFixtureCode(t *testing.T) {
	t.Parallel()

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {localOAuthCode},
		"client_id":     {localOAuthClientID},
		"client_secret": {localOAuthClientSecret},
		"redirect_uri":  {"http://localhost:3002/provider-connectors/hubspot/callback"},
	}
	request := httptest.NewRequest(http.MethodPost, "/oauth/2026-03/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	handleOAuthToken(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected token exchange to return 200, got %d", response.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if payload["access_token"] != validToken || payload["hub_id"] != float64(12345) {
		t.Fatalf("unexpected token response metadata: %#v", payload)
	}
}

func TestStoreUpsertRejectsBatchAtomically(t *testing.T) {
	t.Parallel()

	st := newStore()
	st.seed(seedRequest{Properties: map[string][]property{
		"contacts": {
			{Name: "external_customer_id", HasUniqueValue: true},
			{Name: "email"},
		},
	}})

	_, ok, err := st.upsert("contacts", []batchUpsertInput{
		{ID: "customer-1", IDProperty: "external_customer_id", Properties: map[string]any{"email": "valid@example.com"}},
		{ID: "customer-2", IDProperty: "external_customer_id", Properties: map[string]any{"unknown": "invalid"}},
	})
	if !ok || err == nil {
		t.Fatalf("expected known object batch to fail validation, ok=%t err=%v", ok, err)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.objects["contacts"]) != 0 {
		t.Fatalf("expected rejected batch to leave contacts unchanged, got %#v", st.objects["contacts"])
	}
}

func TestStoreSeedPreservesExistingCustomObjectRecords(t *testing.T) {
	t.Parallel()

	st := newStore()
	const objectTypeID = "2-7282133"
	st.seed(seedRequest{CustomObjects: []customObjectSeed{{
		ObjectTypeID: objectTypeID,
		Name:         "appointments",
		Records: []objectRecord{{
			ID:         "101",
			Properties: map[string]any{"external_id": "appointment-1"},
		}},
	}}})
	st.seed(seedRequest{CustomObjects: []customObjectSeed{{
		ObjectTypeID: objectTypeID,
		Name:         "appointments",
		Records: []objectRecord{{
			ID:         "102",
			Properties: map[string]any{"external_id": "appointment-2"},
		}},
	}}})

	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.objects[objectTypeID]) != 2 {
		t.Fatalf("expected both custom-object records to remain, got %#v", st.objects[objectTypeID])
	}
}

func TestCustomObjectTypeIDValidationMatchesProviderContract(t *testing.T) {
	t.Parallel()

	if !isCustomObjectTypeID("2-7282133") {
		t.Fatal("expected canonical custom object type ID to be accepted")
	}
	for _, invalid := range []string{"2-0", "2-01", "2--1"} {
		if isCustomObjectTypeID(invalid) {
			t.Fatalf("expected invalid custom object type ID %q to be rejected", invalid)
		}
	}
}
