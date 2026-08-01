package apimock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testAPIApp() (*app, http.Handler) {
	a := &app{
		apiKey:   "server-relay-key",
		sessions: map[string]string{"session-id": "csrf-token"},
	}
	mux := http.NewServeMux()
	a.routes(mux)
	return a, mux
}

func TestAPITestRequiresAdministratorSession(t *testing.T) {
	_, handler := testAPIApp()
	request := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{}`))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestAPITestRequiresCSRFToken(t *testing.T) {
	_, handler := testAPIApp()
	request := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{}`))
	request.AddCookie(&http.Cookie{Name: "api_mock_session", Value: "session-id"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestAPITestUsesServerRelayKey(t *testing.T) {
	a, handler := testAPIApp()
	request := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{}`))
	request.AddCookie(&http.Cookie{Name: "api_mock_session", Value: "session-id"})
	request.Header.Set("X-CSRF-Token", "csrf-token")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "messages is required") {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), a.apiKey) {
		t.Fatal("response disclosed the server relay key")
	}
}

func TestModelsRequiresRelayAPIKey(t *testing.T) {
	_, handler := testAPIApp()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestModelsReturnsUniqueSortedEnabledAccountModels(t *testing.T) {
	a, handler := testAPIApp()
	a.state.Accounts = []Account{
		{Model: " gpt-z ", Enabled: true},
		{Model: "gpt-a", Enabled: true},
		{Model: "gpt-z", Enabled: true},
		{Model: "disabled-model", Enabled: false},
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	var result struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Object != "list" || len(result.Data) != 2 || result.Data[0].ID != "gpt-a" || result.Data[1].ID != "gpt-z" {
		t.Fatalf("unexpected response: %+v", result)
	}
}

func TestChooseFiltersAccountsByModel(t *testing.T) {
	a, _ := testAPIApp()
	a.state.Accounts = []Account{
		{ID: "other", Model: "model-b", Enabled: true},
		{ID: "matching", Model: "model-a", Enabled: true},
	}
	account, err := a.choose("model-a")
	if err != nil {
		t.Fatal(err)
	}
	if account.ID != "matching" {
		t.Fatalf("account = %q, want matching", account.ID)
	}
}

func TestChooseRejectsUnavailableModel(t *testing.T) {
	a, _ := testAPIApp()
	a.state.Accounts = []Account{{ID: "account-a", Model: "model-a", Enabled: true}}
	_, err := a.choose("model-b")
	if err == nil || !strings.Contains(err.Error(), "requested model is unavailable") {
		t.Fatalf("error = %v, want unavailable model", err)
	}
}

func TestChatRequiresModel(t *testing.T) {
	a, handler := testAPIApp()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "model is required") {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
}

func TestChatRejectsUnavailableModel(t *testing.T) {
	a, handler := testAPIApp()
	a.state.Accounts = []Account{{ID: "account-a", Model: "model-a", Enabled: true}}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model-b","messages":[]}`))
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "requested model is unavailable") {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
}

func TestChatRoutesNormalizedModelToMatchingAccount(t *testing.T) {
	var receivedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		receivedModel, _ = request["model"].(string)
		jsonOut(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer upstream.Close()

	a, handler := testAPIApp()
	a.dataPath = t.TempDir() + "/api-mock.json"
	a.key = make([]byte, 32)
	a.client = upstream.Client()
	key, err := a.encrypt("upstream-key")
	if err != nil {
		t.Fatal(err)
	}
	a.state.Accounts = []Account{
		{ID: "other", Endpoint: upstream.URL, Model: "other-model", Enabled: true, Key: key},
		{ID: "matching", Endpoint: upstream.URL, Model: " model-a ", Enabled: true, Key: key},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":" model-a ","messages":[]}`))
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if receivedModel != "model-a" {
		t.Fatalf("upstream model = %q, want model-a", receivedModel)
	}
}
