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

func TestSidebarStaysInViewportOnDesktop(t *testing.T) {
	css, err := assets.ReadFile("web/app.css")
	if err != nil {
		t.Fatal(err)
	}
	text := string(css)
	for _, rule := range []string{
		".shell{display:grid;grid-template-columns:230px 1fr;align-items:start;min-height:100vh}",
		".side{position:sticky;top:0;display:flex;flex-direction:column;height:100vh;height:100dvh;overflow-y:auto",
		".side{position:static;display:flex;flex-direction:row;height:auto;min-height:0;overflow:visible",
	} {
		if !strings.Contains(text, rule) {
			t.Fatalf("app.css missing sidebar layout rule %q", rule)
		}
	}
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

func TestChatRejectsOversizedRequestWithStandard413(t *testing.T) {
	a, handler := testAPIApp()
	oversized := `{"model":"model-a","messages":[{"role":"user","content":"` + strings.Repeat("x", maxRequestBodyBytes) + `"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(oversized))
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	errorBody, _ := payload["error"].(map[string]any)
	if errorBody["type"] != "invalid_request_error" || errorBody["code"] != "request_too_large" {
		t.Fatalf("unexpected error: %#v", payload)
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

func TestChatPassesNonStreamingJSONThroughWithoutForcingSSE(t *testing.T) {
	var upstreamRequest map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("X-Request-Id", "request-1")
		jsonOut(w, http.StatusOK, map[string]any{
			"id": "chatcmpl-1", "object": "chat.completion", "model": "model-a",
			"choices": []any{map[string]any{
				"index": 0, "message": map[string]any{"role": "assistant", "content": "OK"}, "finish_reason": "stop",
			}},
		})
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
	a.state.Accounts = []Account{{ID: "account-a", Endpoint: upstream.URL, Model: "model-a", Enabled: true, Key: key}}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model-a","messages":[{"role":"user","content":"Reply OK"}],"stream":false,"stream_options":{"include_usage":true}}`))
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	request.Header.Set("X-API-Mock-WorkBuddy-Compatible", "1")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if upstreamRequest["stream"] != false {
		t.Fatalf("upstream stream = %#v, want false", upstreamRequest["stream"])
	}
	if _, ok := upstreamRequest["stream_options"]; ok {
		t.Fatalf("non-streaming upstream request retained stream_options: %#v", upstreamRequest)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := response.Header().Get("X-Request-Id"); got != "request-1" {
		t.Fatalf("X-Request-Id = %q", got)
	}
	if len(a.state.Records) != 1 || a.state.Records[0].Stream {
		t.Fatalf("unexpected record: %#v", a.state.Records)
	}
}

func TestChatPreservesMaxCompletionTokensUpstream(t *testing.T) {
	var upstreamRequest map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Error(err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"choices": []any{}})
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
	a.state.Accounts = []Account{{ID: "account-a", Endpoint: upstream.URL, Model: "model-a", Enabled: true, Key: key}}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model-a","messages":[],"stream":false,"max_completion_tokens":128,"max_tokens":64}`))
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	request.Header.Set("X-API-Mock-WorkBuddy-Compatible", "1")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || upstreamRequest["max_completion_tokens"] != float64(128) {
		t.Fatalf("status=%d upstream=%#v", response.Code, upstreamRequest)
	}
	if _, exists := upstreamRequest["max_tokens"]; exists {
		t.Fatalf("conflicting max_tokens reached upstream: %#v", upstreamRequest)
	}
}

func TestChatPassesNonStreamingToolJSONThrough(t *testing.T) {
	var upstreamStream any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		upstreamStream = request["stream"]
		jsonOut(w, http.StatusOK, map[string]any{
			"id": "chatcmpl-tools", "object": "chat.completion", "model": "model-a",
			"choices": []any{map[string]any{
				"index": 0,
				"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
					"id": "call_1", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": `{"city":"Paris"}`},
				}}},
				"finish_reason": "tool_calls",
			}},
		})
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
	a.state.Accounts = []Account{{ID: "account-a", Endpoint: upstream.URL, Model: "model-a", Enabled: true, Key: key}}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model-a","messages":[],"stream":false,"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]}`))
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	request.Header.Set("X-API-Mock-WorkBuddy-Compatible", "1")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || upstreamStream != false {
		t.Fatalf("status = %d, upstream stream = %#v, body = %q", response.Code, upstreamStream, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	choice := result["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if !strings.EqualFold(choice["finish_reason"].(string), "tool_calls") || len(message["tool_calls"].([]any)) != 1 {
		t.Fatalf("unexpected completion: %#v", result)
	}
}

func TestChatPreservesSSEWhenClientEnablesStreaming(t *testing.T) {
	var upstreamRequest map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[]}\n\ndata: [DONE]\n\n"))
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
	a.state.Accounts = []Account{{ID: "account-a", Endpoint: upstream.URL, Model: "model-a", Enabled: true, Key: key}}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model-a","messages":[],"stream":true}`))
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	request.Header.Set("X-API-Mock-WorkBuddy-Compatible", "1")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if upstreamRequest["stream"] != true {
		t.Fatalf("upstream stream = %#v, want true", upstreamRequest["stream"])
	}
	options, ok := upstreamRequest["stream_options"].(map[string]any)
	if !ok || options["include_usage"] != true {
		t.Fatalf("missing upstream stream options: %#v", upstreamRequest)
	}
	if got := response.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-cache, no-transform" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := response.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q", got)
	}
	if !strings.Contains(response.Body.String(), "data: [DONE]") {
		t.Fatalf("body = %q", response.Body.String())
	}
	if len(a.state.Records) != 1 || !a.state.Records[0].Stream {
		t.Fatalf("unexpected record: %#v", a.state.Records)
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
