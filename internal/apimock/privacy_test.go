package apimock

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestValidateInjectedTextRejectsLocalPaths(t *testing.T) {
	for _, value := range []string{
		"C:\\Users\\alice\\skill.md",
		"/home/alice/skill.md",
		"\\\\host\\share\\skill.md",
	} {
		if err := validateInjectedText("test", value); err == nil {
			t.Fatalf("validateInjectedText(%q) accepted a local path", value)
		}
	}
}

func TestValidateInjectedTextAllowsNeutralDescription(t *testing.T) {
	if err := validateInjectedText("test", "Execute a skill with its name and optional arguments."); err != nil {
		t.Fatalf("validateInjectedText() = %v", err)
	}
}

func TestLoadModelInstructionsUsesPrivateRuntimeFile(t *testing.T) {
	path := t.TempDir() + "/model_instructions.private.md"
	private := strings.Repeat("private runtime instructions that remain inside the deployment data volume. ", 2)
	if err := os.WriteFile(path, []byte(private), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("API_MOCK_MODEL_INSTRUCTIONS_FILE", path)
	got, err := loadModelInstructions()
	if err != nil || got != strings.TrimSpace(private) {
		t.Fatalf("loadModelInstructions() = %q, %v", got, err)
	}
}

func TestLoadWorkBuddyProfileUsesPrivateRuntimeFile(t *testing.T) {
	path := t.TempDir() + "/workbuddy_profile.private.json"
	if err := os.WriteFile(path, []byte(`{"headers":{"User-Agent":"WorkBuddy/test","X-Agent-Purpose":"conversation"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("API_MOCK_WORKBUDDY_PROFILE_FILE", path)
	profile, err := loadWorkBuddyProfile()
	if err != nil || profile["User-Agent"] != "WorkBuddy/test" {
		t.Fatalf("loadWorkBuddyProfile() = %#v, %v", profile, err)
	}
}

func TestApplyWorkBuddyRequestProfile(t *testing.T) {
	request := map[string]any{
		"model":    "gpt-5.6-sol",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"stream":   false,
	}

	applyWorkBuddyRequestProfile(request, "system contract")

	if request["stream"] != true || request["reasoning_effort"] != "low" || request["temperature"] != 1 {
		t.Fatalf("unexpected WorkBuddy request profile: %#v", request)
	}
	streamOptions, ok := request["stream_options"].(map[string]any)
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("missing stream usage option: %#v", request["stream_options"])
	}
	if _, ok := request["tools"]; ok {
		t.Fatalf("unexpected injected tools: %#v", request["tools"])
	}
	if _, ok := request["tool_choice"]; ok {
		t.Fatalf("unexpected injected tool choice: %#v", request["tool_choice"])
	}
	messages := request["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("unexpected messages: %#v", messages)
	}
	system := messages[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "system contract" {
		t.Fatalf("unexpected system instructions: %#v", system)
	}
	if messages[1].(map[string]any)["content"] != "hello" {
		t.Fatalf("unexpected WorkBuddy user content: %#v", messages[0])
	}
}

func TestApplyWorkBuddyRequestProfileDoesNotDuplicateModelInstructions(t *testing.T) {
	request := map[string]any{
		"messages": []any{map[string]any{
			"role":    "system",
			"content": []any{map[string]any{"type": "text", "text": "system contract"}},
		}},
	}

	applyWorkBuddyRequestProfile(request, "system contract")

	messages := request["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("model instructions duplicated: %#v", messages)
	}
}

func TestApplyWorkBuddyRequestProfilePreservesCallerTools(t *testing.T) {
	customTools := []any{map[string]any{"type": "function", "function": map[string]any{"name": "custom_tool"}}}
	request := map[string]any{
		"messages":    []any{map[string]any{"role": "user", "content": "hello"}},
		"tools":       customTools,
		"tool_choice": "auto",
	}

	applyWorkBuddyRequestProfile(request, "system contract")

	if request["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"] != "custom_tool" || request["tool_choice"] != "auto" {
		t.Fatalf("caller tool settings were changed: %#v", request)
	}
}

func TestWorkBuddyHeadersIncludeConfiguredUserContext(t *testing.T) {
	headers := workBuddyHeaders("relay-key", "00000000-0000-0000-0000-000000000000")
	for _, name := range []string{"Acp-Connection-ID", "X-User-ID", "X-Conversation-ID", "X-Conversation-Message-ID", "X-Conversation-Request-ID", "X-Request-ID", "X-B3-ParentSpanID"} {
		if headers[name] == "" {
			t.Fatalf("missing %s", name)
		}
	}
	if got := strings.Count(headers["B3"], "-"); got != 3 {
		t.Fatalf("unexpected B3 trace shape: %q", headers["B3"])
	}
	if headers["X-User-ID"] != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("unexpected user ID: %q", headers["X-User-ID"])
	}
}

func TestNativeWorkBuddyConversationPreservesClientProfile(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("X-Agent-Purpose", "conversation")
	request.Header.Set("X-CodeBuddy-Request", "1")
	request.Header.Set("User-Agent", "WorkBuddy/5.3.5")
	request.Header.Set("X-IDE-Name", "WorkBuddy")
	payload := map[string]any{"tools": make([]any, 20)}
	if !isNativeWorkBuddyConversation(request, payload) {
		t.Fatal("expected a complete native WorkBuddy request to be preserved")
	}
}

func TestNativeWorkBuddyConversationDetectsClientProfile(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("X-Agent-Purpose", "conversation")
	request.Header.Set("X-CodeBuddy-Request", "1")
	request.Header.Set("User-Agent", "WorkBuddy/5.3.5")
	request.Header.Set("X-IDE-Name", "WorkBuddy")
	payload := map[string]any{"model": "client-model", "tools": make([]any, 20)}
	if !isNativeWorkBuddyConversation(request, payload) {
		t.Fatalf("native WorkBuddy profile was not detected: %#v", payload)
	}
}

func TestNativeWorkBuddyProfileRemovesWorkBuddyToolSettings(t *testing.T) {
	payload := map[string]any{"tools": []any{map[string]any{"type": "function"}}, "tool_choice": "none"}
	applyNativeWorkBuddyProfile(payload)
	if _, ok := payload["tool_choice"]; ok {
		t.Fatalf("native tool choice must be removed: %#v", payload)
	}
	if _, ok := payload["tools"]; ok {
		t.Fatalf("native tools must be removed: %#v", payload)
	}
}

func TestIncompleteWorkBuddyConversationUsesRelayProfile(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("X-Agent-Purpose", "conversation")
	request.Header.Set("X-CodeBuddy-Request", "1")
	payload := map[string]any{"tools": make([]any, 1)}
	if isNativeWorkBuddyConversation(request, payload) {
		t.Fatal("incomplete client profile must use the relay profile")
	}
}

func TestOnlyNativeWorkBuddyCanPreserveCallerFingerprint(t *testing.T) {
	for _, caller := range []struct {
		name      string
		userAgent string
		ideName   string
	}{
		{name: "agent client", userAgent: "agent-client/1.0", ideName: "Agent Client"},
		{name: "unknown client", userAgent: "custom-client/1.0", ideName: "Custom Client"},
	} {
		t.Run(caller.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			request.Header.Set("X-Agent-Purpose", "conversation")
			request.Header.Set("X-CodeBuddy-Request", "1")
			request.Header.Set("User-Agent", caller.userAgent)
			request.Header.Set("X-IDE-Name", caller.ideName)

			if isNativeWorkBuddyConversation(request, map[string]any{"tools": make([]any, 20)}) {
				t.Fatal("non-WorkBuddy caller must use the generated WorkBuddy profile")
			}
		})
	}
}

func TestNativeWorkBuddyHeadersOverrideGeneratedProfile(t *testing.T) {
	headers := workBuddyHeaders("upstream-key", "configured-user")
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("User-Agent", "WorkBuddy/current")
	request.Header.Set("X-User-ID", "native-user")
	request.Header.Set("X-Trace-ID", "native-trace")
	overlayNativeWorkBuddyHeaders(headers, request.Header)
	if headers["User-Agent"] != "WorkBuddy/current" || headers["X-User-ID"] != "native-user" || headers["X-Trace-ID"] != "native-trace" {
		t.Fatalf("native WorkBuddy headers were not preserved: %#v", headers)
	}
	if headers["Authorization"] != "Bearer upstream-key" || headers["X-API-Key"] != "upstream-key" {
		t.Fatalf("upstream authentication was changed: %#v", headers)
	}
}

func TestPrivateWorkBuddyProfileOnlyOverlaysAllowedHeaders(t *testing.T) {
	headers := workBuddyHeaders("upstream-key", "configured-user")
	overlayWorkBuddyProfile(headers, map[string]string{
		"User-Agent":    "WorkBuddy/private",
		"X-Trace-ID":    "private-trace",
		"Authorization": "Bearer attacker-key",
		"X-API-Key":     "attacker-key",
	})
	if headers["User-Agent"] != "WorkBuddy/private" || headers["X-Trace-ID"] != "private-trace" {
		t.Fatalf("allowed private profile headers were not applied: %#v", headers)
	}
	if headers["Authorization"] != "Bearer upstream-key" || headers["X-API-Key"] != "upstream-key" {
		t.Fatalf("upstream authentication was changed: %#v", headers)
	}
}

func TestPrivateWorkBuddyProfileMatchesCanonicalHeaderCasing(t *testing.T) {
	headers := workBuddyHeaders("upstream-key", "configured-user")
	overlayWorkBuddyProfile(headers, map[string]string{
		"X-Codebuddy-Request": "private-request",
		"X-Stainless-Os":      "private-os",
	})
	if headers["X-CodeBuddy-Request"] != "private-request" || headers["X-Stainless-OS"] != "private-os" {
		t.Fatalf("canonical private profile headers were not applied: %#v", headers)
	}
}

func TestConversationTopicReturnsLocalStreamWithoutDecryptingAccount(t *testing.T) {
	dataDir := t.TempDir()
	a := &app{
		dataPath: dataDir + "/api-mock.json",
		apiKey:   "relay-key",
		state: state{Accounts: []Account{{
			ID:       "account-1",
			Label:    "test",
			Endpoint: "https://example.invalid/v1",
			Model:    "gpt-5.6-sol",
			Enabled:  true,
			Key:      "not-valid-ciphertext",
		}}},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt","stream":true,"messages":[{"role":"user","content":"private prompt"}]}`))
	request.Header.Set("Authorization", "Bearer relay-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Agent-Purpose", "conversation_topic")
	response := httptest.NewRecorder()

	a.chat(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("unexpected content type: %q", got)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"content":"New conversation"`) || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("unexpected topic stream: %q", body)
	}
	if strings.Contains(body, "private prompt") {
		t.Fatalf("topic response leaked request content: %q", body)
	}
	if len(a.state.Records) != 1 || a.state.Records[0].Status != "succeeded" || a.state.Records[0].HTTPStatus != http.StatusOK {
		t.Fatalf("unexpected request record: %#v", a.state.Records)
	}
}

func TestSSEErrorDetectorHandlesChunkedEventLines(t *testing.T) {
	detector := &sseErrorDetector{}
	for _, chunk := range []string{"data: {\"choices\":[]}\n\neve", "nt: error\ndata: {\"error\":{\"type\":\"rate_limit_error\"}}\n\n"} {
		if _, err := detector.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if !detector.failed {
		t.Fatal("expected SSE error event to be detected")
	}
}

func TestSSEErrorDetectorIgnoresErrorTextInContent(t *testing.T) {
	detector := &sseErrorDetector{}
	_, _ = detector.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"event: error\"}}]}\n\n"))
	if detector.failed {
		t.Fatal("content text must not be classified as an SSE error event")
	}
}
