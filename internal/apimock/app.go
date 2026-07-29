package apimock

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ramudaderuta/Buddy-API-mock/prompts"
)

//go:embed web/*
var assets embed.FS

type Account struct {
	ID, Label, Endpoint, Model string
	Enabled                    bool
	Key                        string `json:"key"`
	CreatedAt, LastUsedAt      time.Time
}
type Record struct {
	ID, AccountID, AccountLabel, Model, Status string
	HTTPStatus                                 int
	Stream                                     bool
	DurationMS                                 int64
	At                                         time.Time
}
type state struct {
	Strategy string    `json:"strategy"`
	Cursor   int       `json:"cursor"`
	Accounts []Account `json:"accounts"`
	Records  []Record  `json:"records"`
}
type app struct {
	mu               sync.Mutex
	dataPath         string
	key              []byte
	password         string
	apiKey           string
	state            state
	sessions         map[string]string
	client           *http.Client
	skillDescription string
	workBuddyUserID  string
}

func Run() error {
	listen := env("API_MOCK_LISTEN", "127.0.0.1:13100")
	password := os.Getenv("API_MOCK_ADMIN_PASSWORD")
	if password == "" {
		return errors.New("API_MOCK_ADMIN_PASSWORD is required")
	}
	apiKey := strings.TrimSpace(os.Getenv("API_MOCK_API_KEY"))
	if apiKey == "" {
		return errors.New("API_MOCK_API_KEY is required")
	}
	dataDir := env("API_MOCK_DATA_DIR", "./data")
	workBuddyUserID := strings.TrimSpace(os.Getenv("API_MOCK_WORKBUDDY_USER_ID"))
	skillDescription, err := loadSkillDescription()
	if err != nil {
		return err
	}
	if err := validateInjectedText("system instructions", prompts.ModelInstructions); err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	key, err := loadKey(filepath.Join(dataDir, "api-mock.key"))
	if err != nil {
		return err
	}
	a := &app{dataPath: filepath.Join(dataDir, "api-mock.json"), key: key, password: password, apiKey: apiKey, sessions: map[string]string{}, client: &http.Client{Timeout: 10 * time.Minute}, skillDescription: skillDescription, workBuddyUserID: workBuddyUserID}
	if err := a.load(); err != nil {
		return err
	}
	mux := http.NewServeMux()
	a.routes(mux)
	return http.ListenAndServe(listen, mux)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// loadSkillDescription prefers an optional host override file, otherwise the
// description embedded from prompts/skill_description.txt.
func loadSkillDescription() (string, error) {
	var text string
	if path := strings.TrimSpace(os.Getenv("API_MOCK_SKILL_DESCRIPTION_FILE")); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", errors.New("API_MOCK_SKILL_DESCRIPTION_FILE could not be read")
		}
		text = string(b)
	} else {
		text = prompts.SkillDescription
	}
	text = sanitizeSkillDescription(text)
	if len(text) < 100 {
		return "", errors.New("Skill description is missing or too short")
	}
	if err := validateInjectedText("Skill description", text); err != nil {
		return "", err
	}
	return text, nil
}

var skillLocationPattern = regexp.MustCompile(`(?im)\s*\(location:\s*[^\r\n)]*\)`)
var localPathPattern = regexp.MustCompile(`(?i)(?:[a-z]:[\\/]|\\\\(?:[^\\/]+)\\|/(?:home|users|private|var|tmp)(?:/|$))`)

func sanitizeSkillDescription(text string) string {
	return strings.TrimSpace(skillLocationPattern.ReplaceAllStringFunc(text, redactSkillLocation))
}

func redactSkillLocation(location string) string {
	const prefix = " (location: "
	const suffix = ")"
	padding := utf8.RuneCountInString(location) - utf8.RuneCountInString(prefix) - utf8.RuneCountInString(suffix)
	if padding < 1 {
		return prefix + "redacted" + suffix
	}
	return prefix + strings.Repeat("x", padding) + suffix
}

func validateInjectedText(name, text string) error {
	if localPathPattern.MatchString(text) {
		return fmt.Errorf("%s must not contain a local filesystem path", name)
	}
	hostname, err := os.Hostname()
	if err == nil && len(hostname) > 2 && strings.Contains(strings.ToLower(text), strings.ToLower(hostname)) {
		return fmt.Errorf("%s must not contain the local hostname", name)
	}
	return nil
}
func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func loadKey(path string) ([]byte, error) {
	if b, e := os.ReadFile(path); e == nil {
		return base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(b)))
	}
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return nil, e
	}
	if e := os.WriteFile(path, []byte(base64.RawStdEncoding.EncodeToString(b)+"\n"), 0o600); e != nil {
		return nil, e
	}
	return b, nil
}
func (a *app) encrypt(value string) (string, error) {
	block, e := aes.NewCipher(a.key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	nonce := make([]byte, g.NonceSize())
	_, _ = rand.Read(nonce)
	return base64.RawStdEncoding.EncodeToString(append(nonce, g.Seal(nil, nonce, []byte(value), nil)...)), nil
}
func (a *app) decrypt(value string) (string, error) {
	raw, e := base64.RawStdEncoding.DecodeString(value)
	if e != nil {
		return "", e
	}
	block, e := aes.NewCipher(a.key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	if len(raw) < g.NonceSize() {
		return "", errors.New("invalid account key")
	}
	out, e := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], nil)
	return string(out), e
}
func (a *app) load() error {
	b, e := os.ReadFile(a.dataPath)
	if os.IsNotExist(e) {
		a.state.Strategy = "fill_first"
		return nil
	}
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, &a.state); e != nil {
		return e
	}
	if a.state.Strategy != "round_robin" {
		a.state.Strategy = "fill_first"
	}
	if a.state.Accounts == nil {
		a.state.Accounts = []Account{}
	}
	if a.state.Records == nil {
		a.state.Records = []Record{}
	}
	return nil
}
func (a *app) save() error {
	b, e := json.MarshalIndent(a.state, "", "  ")
	if e != nil {
		return e
	}
	tmp := a.dataPath + ".tmp"
	if e = os.WriteFile(tmp, b, 0o600); e != nil {
		return e
	}
	return os.Rename(tmp, a.dataPath)
}

func (a *app) routes(m *http.ServeMux) {
	m.HandleFunc("POST /v1/chat/completions", a.chat)
	m.HandleFunc("POST /api/login", a.login)
	m.HandleFunc("POST /api/logout", a.logout)
	m.HandleFunc("GET /api/session", a.session)
	m.HandleFunc("GET /api/accounts", a.guard(a.accounts))
	m.HandleFunc("POST /api/accounts", a.guard(a.accounts))
	m.HandleFunc("DELETE /api/accounts/{id}", a.guard(a.account))
	m.HandleFunc("GET /api/records", a.guard(a.records))
	m.HandleFunc("POST /api/strategy", a.guard(a.strategy))
	m.HandleFunc("/", a.web)
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func appErr(w http.ResponseWriter, status int, message string) {
	jsonOut(w, status, map[string]any{"error": map[string]string{"message": message, "type": "invalid_request_error"}})
}
func (a *app) web(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	b, e := assets.ReadFile("web/" + path)
	if e != nil {
		b, _ = assets.ReadFile("web/index.html")
	}
	if strings.HasSuffix(path, ".css") {
		w.Header().Set("Content-Type", "text/css")
	}
	if strings.HasSuffix(path, ".js") {
		w.Header().Set("Content-Type", "text/javascript")
	}
	_, _ = w.Write(b)
}
func (a *app) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Password != a.password {
		appErr(w, 401, "invalid administrator password")
		return
	}
	id, csrf := randomString(24), randomString(24)
	a.mu.Lock()
	a.sessions[id] = csrf
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "api_mock_session", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	jsonOut(w, 200, map[string]string{"csrf": csrf})
}
func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if c, _ := r.Cookie("api_mock_session"); c != nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "api_mock_session", Value: "", Path: "/", MaxAge: -1})
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *app) session(w http.ResponseWriter, r *http.Request) {
	_, csrf := a.auth(r)
	jsonOut(w, 200, map[string]any{"authenticated": csrf != "", "csrf": csrf})
}
func (a *app) auth(r *http.Request) (string, string) {
	c, e := r.Cookie("api_mock_session")
	if e != nil {
		return "", ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return c.Value, a.sessions[c.Value]
}
func (a *app) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, csrf := a.auth(r)
		if csrf == "" {
			appErr(w, 401, "administrator login required")
			return
		}
		if r.Method != "GET" && r.Header.Get("X-CSRF-Token") != csrf {
			appErr(w, 403, "invalid CSRF token")
			return
		}
		next(w, r)
	}
}
func publicAccount(x Account) map[string]any {
	return map[string]any{"id": x.ID, "label": x.Label, "endpoint": x.Endpoint, "model": x.Model, "enabled": x.Enabled, "createdAt": x.CreatedAt, "lastUsedAt": x.LastUsedAt}
}
func (a *app) accounts(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == "GET" {
		out := make([]map[string]any, 0, len(a.state.Accounts))
		for _, x := range a.state.Accounts {
			out = append(out, publicAccount(x))
		}
		jsonOut(w, 200, map[string]any{"strategy": a.state.Strategy, "data": out})
		return
	}
	var in struct {
		Label, Endpoint, APIKey, Model string
		Enabled                        bool
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Label == "" || in.APIKey == "" || in.Model == "" {
		appErr(w, 400, "label, API key, and model are required")
		return
	}
	endpoint, e := normalizeEndpoint(in.Endpoint)
	if e != nil {
		appErr(w, 400, e.Error())
		return
	}
	key, e := a.encrypt(in.APIKey)
	if e != nil {
		appErr(w, 500, "unable to encrypt account")
		return
	}
	a.state.Accounts = append(a.state.Accounts, Account{ID: randomString(9), Label: in.Label, Endpoint: endpoint, Model: in.Model, Enabled: in.Enabled, Key: key, CreatedAt: time.Now().UTC()})
	_ = a.save()
	jsonOut(w, 201, map[string]bool{"ok": true})
}
func (a *app) account(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := r.PathValue("id")
	for i, x := range a.state.Accounts {
		if x.ID == id {
			a.state.Accounts = append(a.state.Accounts[:i], a.state.Accounts[i+1:]...)
			_ = a.save()
			jsonOut(w, 200, map[string]bool{"ok": true})
			return
		}
	}
	appErr(w, 404, "account not found")
}
func (a *app) records(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.state.Records
	if out == nil {
		out = []Record{}
	}
	jsonOut(w, 200, map[string]any{"data": out})
}
func (a *app) strategy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Strategy string `json:"strategy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		appErr(w, 400, "invalid strategy payload")
		return
	}
	if in.Strategy != "fill_first" && in.Strategy != "round_robin" {
		appErr(w, 400, "strategy must be fill_first or round_robin")
		return
	}
	a.mu.Lock()
	a.state.Strategy = in.Strategy
	err := a.save()
	current := a.state.Strategy
	a.mu.Unlock()
	if err != nil {
		appErr(w, 500, "unable to persist strategy")
		return
	}
	jsonOut(w, 200, map[string]any{"ok": true, "strategy": current})
}
func normalizeEndpoint(raw string) (string, error) {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", errors.New("endpoint is required")
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" {
		return "", errors.New("endpoint must be an HTTPS base URL")
	}
	u.Path = strings.TrimSuffix(u.Path, "/chat/completions")
	return strings.TrimSuffix(u.String(), "/"), nil
}
func (a *app) choose() (Account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	available := make([]int, 0)
	for i, x := range a.state.Accounts {
		if x.Enabled {
			available = append(available, i)
		}
	}
	if len(available) == 0 {
		return Account{}, errors.New("account pool exhausted")
	}
	index := available[0]
	if a.state.Strategy == "round_robin" {
		index = available[a.state.Cursor%len(available)]
		a.state.Cursor++
		_ = a.save()
	}
	return a.state.Accounts[index], nil
}
func clientAPIKey(r *http.Request) string {
	if raw := strings.TrimSpace(r.Header.Get("Authorization")); raw != "" {
		const prefix = "Bearer "
		if len(raw) >= len(prefix) && strings.EqualFold(raw[:len(prefix)], prefix) {
			return strings.TrimSpace(raw[len(prefix):])
		}
	}
	if v := strings.TrimSpace(r.Header.Get("X-API-Key")); v != "" {
		return v
	}
	return strings.TrimSpace(r.URL.Query().Get("api_key"))
}

func (a *app) authorizeAPI(r *http.Request) bool {
	provided := clientAPIKey(r)
	if provided == "" || a.apiKey == "" {
		return false
	}
	if len(provided) != len(a.apiKey) {
		// Keep compare length-stable against the configured key.
		_ = subtle.ConstantTimeCompare([]byte(a.apiKey), []byte(a.apiKey))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(a.apiKey)) == 1
}

func (a *app) chat(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeAPI(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		appErr(w, 401, "invalid api key")
		return
	}
	body, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if e != nil {
		appErr(w, 400, "invalid request body")
		return
	}
	var request map[string]any
	if json.Unmarshal(body, &request) != nil || request["messages"] == nil {
		appErr(w, 400, "messages is required")
		return
	}
	account, e := a.choose()
	if e != nil {
		appErr(w, 503, e.Error())
		return
	}
	request["model"] = account.Model
	if _, ok := request["messages"].([]any); !ok {
		appErr(w, 400, "messages is required")
		return
	}
	started := time.Now()
	if isWorkBuddyConversationTopic(r) {
		writeWorkBuddyConversationTopic(w, account.Model)
		a.record(account, request, http.StatusOK, true, started)
		return
	}
	applyWorkBuddyRequestProfile(request, a.skillDescription)
	body, _ = json.Marshal(request)
	key, e := a.decrypt(account.Key)
	if e != nil {
		appErr(w, 500, "account key unavailable")
		return
	}
	upstream, e := http.NewRequestWithContext(r.Context(), http.MethodPost, account.Endpoint+"/chat/completions", strings.NewReader(string(body)))
	if e != nil {
		appErr(w, 502, "invalid upstream request")
		return
	}
	for k, v := range workBuddyHeaders(key, a.workBuddyUserID) {
		upstream.Header.Set(k, v)
	}
	upstream.Header.Set("Content-Type", "application/json")
	response, e := a.client.Do(upstream)
	status := 0
	succeeded := false
	if e == nil {
		status = response.StatusCode
		for _, h := range []string{"Content-Type", "Cache-Control", "X-Request-Id"} {
			if v := response.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.WriteHeader(response.StatusCode)
		detector := &sseErrorDetector{}
		reader := io.Reader(response.Body)
		if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
			reader = io.TeeReader(response.Body, detector)
		}
		_, _ = io.Copy(w, reader)
		response.Body.Close()
		succeeded = status < 400 && !detector.failed
	} else {
		appErr(w, 502, "upstream request failed")
	}
	a.record(account, request, status, succeeded, started)
}

func isWorkBuddyConversationTopic(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Agent-Purpose")), "conversation_topic")
}

func writeWorkBuddyConversationTopic(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	chunk := map[string]any{
		"id":      "chatcmpl_" + randomString(12),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{
				"role":    "assistant",
				"content": "New conversation",
			},
			"finish_reason": "stop",
		}},
	}
	encoded, _ := json.Marshal(chunk)
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (a *app) record(account Account, request map[string]any, status int, succeeded bool, started time.Time) {
	a.mu.Lock()
	for i := range a.state.Accounts {
		if a.state.Accounts[i].ID == account.ID {
			a.state.Accounts[i].LastUsedAt = time.Now().UTC()
		}
	}
	a.state.Records = append([]Record{{ID: randomString(8), AccountID: account.ID, AccountLabel: account.Label, Model: account.Model, Status: map[bool]string{true: "succeeded", false: "failed"}[succeeded], HTTPStatus: status, Stream: request["stream"] == true, DurationMS: time.Since(started).Milliseconds(), At: time.Now().UTC()}}, a.state.Records...)
	if len(a.state.Records) > 100 {
		a.state.Records = a.state.Records[:100]
	}
	_ = a.save()
	a.mu.Unlock()
}

type sseErrorDetector struct {
	line   []byte
	failed bool
}

func (d *sseErrorDetector) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			d.inspectLine()
			d.line = d.line[:0]
			continue
		}
		if len(d.line) < 512 {
			d.line = append(d.line, b)
		}
	}
	return len(p), nil
}

func (d *sseErrorDetector) inspectLine() {
	line := strings.TrimSpace(string(d.line))
	if strings.EqualFold(line, "event: error") || strings.HasPrefix(line, `data: {"error":`) {
		d.failed = true
	}
}

func applyWorkBuddyRequestProfile(request map[string]any, skillDescription string) {
	normalizeWorkBuddyMessages(request)
	request["stream"] = true
	request["reasoning_effort"] = "low"
	request["temperature"] = 1
	request["stream_options"] = map[string]any{"include_usage": true}
	request["tools"] = []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "buddy_skill",
			"description": skillDescription,
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"skill":   map[string]any{"type": "string", "description": "The skill name."},
					"command": map[string]any{"type": "string", "description": "Legacy skill name without arguments."},
					"args":    map[string]any{"type": "string", "description": "Optional arguments for the skill."},
				},
				"additionalProperties": false,
				"$schema":              "http://json-schema.org/draft-07/schema#",
			},
			"strict": false,
		},
	}}
	request["tool_choice"] = "none"
}

func normalizeWorkBuddyMessages(request map[string]any) {
	messages, ok := request["messages"].([]any)
	if !ok {
		return
	}
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok || message["role"] != "user" {
			continue
		}
		content, ok := message["content"].(string)
		if !ok {
			continue
		}
		message["content"] = []any{map[string]any{"type": "text", "text": content}}
	}
}

func workBuddyHeaders(key, userID string) map[string]string {
	acpConnectionID := randomUUID()
	conversationID := randomUUID()
	requestID := strings.ReplaceAll(randomUUID(), "-", "")
	traceID := randomHex(16)
	spanID := randomHex(8)
	parentSpanID := randomHex(8)
	headers := map[string]string{
		"Authorization":               "Bearer " + key,
		"X-API-Key":                   key,
		"Accept":                      "application/json",
		"User-Agent":                  "WorkBuddy/5.3.5 WorkBuddy/5.3.5 CLI/2.115.0",
		"X-Agent-Intent":              "craft",
		"X-Agent-Purpose":             "conversation",
		"X-CodeBuddy-Request":         "1",
		"X-Domain":                    "www.codebuddy.cn",
		"X-IDE-Name":                  "WorkBuddy",
		"X-IDE-Type":                  "WorkBuddy",
		"X-IDE-Version":               "5.3.5",
		"X-Product":                   "SaaS",
		"X-Requested-With":            "XMLHttpRequest",
		"X-Stainless-Arch":            "x64",
		"X-Stainless-Lang":            "js",
		"X-Stainless-OS":              "Windows",
		"X-Stainless-Package-Version": "6.25.0",
		"X-Stainless-Retry-Count":     "0",
		"X-Stainless-Runtime":         "node",
		"X-Stainless-Runtime-Version": "v22.21.1",
		"Acp-Connection-ID":           acpConnectionID,
		"B3":                          traceID + "-" + spanID + "-1-" + parentSpanID,
		"Traceparent":                 "00-" + traceID + "-" + spanID + "-01",
		"X-B3-ParentSpanID":           parentSpanID,
		"X-B3-Sampled":                "1",
		"X-B3-SpanID":                 spanID,
		"X-B3-TraceID":                traceID,
		"X-Trace-ID":                  traceID,
	}
	if userID != "" {
		headers["X-User-ID"] = userID
		headers["X-Conversation-ID"] = conversationID
		headers["X-Conversation-Message-ID"] = requestID
		headers["X-Conversation-Request-ID"] = requestID
		headers["X-Request-ID"] = requestID
	}
	return headers
}

func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
func sortedHeaders(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for k := range values {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
