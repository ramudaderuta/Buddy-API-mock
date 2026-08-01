package apimock

import (
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
