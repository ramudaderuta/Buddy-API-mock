package apimock

import (
	"github.com/ramudaderuta/Buddy-API-mock/prompts"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeSkillDescriptionRemovesLocationSuffixes(t *testing.T) {
	input := "- skill: useful skill (location: C:\\Users\\alice\\AppData\\Local\\skill.md)\n- other: stays"
	got := sanitizeSkillDescription(input)
	if strings.Contains(got, "C:\\Users\\alice") {
		t.Fatalf("sanitizeSkillDescription() retained local path: %q", got)
	}
	if utf8.RuneCountInString(got) != utf8.RuneCountInString(input) {
		t.Fatalf("sanitizeSkillDescription() changed length: got %d, want %d", utf8.RuneCountInString(got), utf8.RuneCountInString(input))
	}
	if !strings.Contains(got, "(location: ") {
		t.Fatalf("sanitizeSkillDescription() removed location metadata: %q", got)
	}
}

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

func TestEmbeddedSkillDescriptionMeetsWorkBuddyCompatibilityFloor(t *testing.T) {
	description := sanitizeSkillDescription(prompts.SkillDescription)
	if got := utf8.RuneCountInString(description); got < 6144 {
		t.Fatalf("embedded Skill description is too short for the WorkBuddy profile: %d runes", got)
	}
	if err := validateInjectedText("Skill description", description); err != nil {
		t.Fatalf("embedded Skill description is unsafe: %v", err)
	}
}

func TestApplyWorkBuddyRequestProfile(t *testing.T) {
	request := map[string]any{
		"model":    "gpt-5.6-sol",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"stream":   false,
	}

	applyWorkBuddyRequestProfile(request, "skill catalog")

	if request["stream"] != true || request["reasoning_effort"] != "low" || request["temperature"] != 1 {
		t.Fatalf("unexpected WorkBuddy request profile: %#v", request)
	}
	streamOptions, ok := request["stream_options"].(map[string]any)
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("missing stream usage option: %#v", request["stream_options"])
	}
	tools, ok := request["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("unexpected tools: %#v", request["tools"])
	}
	function := tools[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != "buddy_skill" || function["description"] != "skill catalog" {
		t.Fatalf("unexpected skill function: %#v", function)
	}
	parameters := function["parameters"].(map[string]any)
	if parameters["$schema"] != "http://json-schema.org/draft-07/schema#" || parameters["additionalProperties"] != false {
		t.Fatalf("unexpected skill schema: %#v", parameters)
	}
	if request["tool_choice"] != "none" {
		t.Fatalf("unexpected tool choice: %#v", request["tool_choice"])
	}
	messages := request["messages"].([]any)
	content, ok := messages[0].(map[string]any)["content"].([]any)
	if !ok || len(content) != 1 || content[0].(map[string]any)["type"] != "text" || content[0].(map[string]any)["text"] != "hello" {
		t.Fatalf("unexpected WorkBuddy user content: %#v", messages[0])
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
