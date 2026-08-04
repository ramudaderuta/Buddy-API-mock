package apimock

import (
	"context"
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
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

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
	Outcome, FailureClass                      string
	HTTPStatus                                 int
	Stream, Completed                          bool
	DurationMS                                 int64
	At                                         time.Time
}

type requestResult struct {
	Outcome      string
	FailureClass string
	HTTPStatus   int
	Completed    bool
}

const (
	outcomeSucceeded                 = "succeeded"
	outcomeClientCanceled            = "client_canceled"
	outcomeUpstreamHTTPError         = "upstream_http_error"
	outcomeUpstreamSSEError          = "upstream_sse_error"
	outcomeUpstreamStreamInterrupted = "upstream_stream_interrupted"
	outcomeStreamIdleTimeout         = "stream_idle_timeout"
	outcomeRequestFailed             = "request_failed"
)

type state struct {
	Strategy string    `json:"strategy"`
	Cursor   int       `json:"cursor"`
	Accounts []Account `json:"accounts"`
	Records  []Record  `json:"records"`
}
type app struct {
	mu                 sync.Mutex
	dataPath           string
	key                []byte
	password           string
	apiKey             string
	state              state
	sessions           map[string]string
	client             *http.Client
	modelInstructions  string
	workBuddyProfile   map[string]string
	workBuddyUserID    string
	workBuddyTools     []any
	outgoingCaptureDir string
	retryWait          func(context.Context, time.Duration) bool
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
	modelInstructions, err := loadModelInstructions()
	if err != nil {
		return err
	}
	workBuddyProfile, err := loadWorkBuddyProfile()
	if err != nil {
		return err
	}
	workBuddyTools, err := loadWorkBuddyTools()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	key, err := loadKey(filepath.Join(dataDir, "api-mock.key"))
	if err != nil {
		return err
	}
	a := &app{dataPath: filepath.Join(dataDir, "api-mock.json"), key: key, password: password, apiKey: apiKey, sessions: map[string]string{}, client: newUpstreamHTTPClient(), modelInstructions: modelInstructions, workBuddyProfile: workBuddyProfile, workBuddyUserID: workBuddyUserID, workBuddyTools: workBuddyTools, outgoingCaptureDir: strings.TrimSpace(os.Getenv("API_MOCK_OUTGOING_CAPTURE_DIR"))}
	if err := a.load(); err != nil {
		return err
	}
	mux := http.NewServeMux()
	a.routes(mux)
	return newHTTPServer(listen, mux).ListenAndServe()
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

var localPathPattern = regexp.MustCompile(`(?i)(?:[a-z]:[\\/]|\\\\(?:[^\\/]+)\\|/(?:home|users|private|var|tmp)(?:/|$))`)

var modelInstructionTemplateVariables = map[string]string{
	"ArtifactDirectoryPath":        "API_MOCK_PROMPT_ARTIFACT_DIRECTORY_PATH",
	"BinaryContext":                "API_MOCK_PROMPT_BINARY_CONTEXT",
	"ClawMemory_1":                 "API_MOCK_PROMPT_CLAW_MEMORY_1",
	"ClawMemory_2":                 "API_MOCK_PROMPT_CLAW_MEMORY_2",
	"ClawMemory_3":                 "API_MOCK_PROMPT_CLAW_MEMORY_3",
	"dataFolderName":               "API_MOCK_PROMPT_DATA_FOLDER_NAME",
	"ExpertManagement":             "API_MOCK_PROMPT_EXPERT_MANAGEMENT",
	"modelName":                    "API_MOCK_PROMPT_MODEL_NAME",
	"PluginAgentPrompt":            "API_MOCK_PROMPT_PLUGIN_AGENT_PROMPT",
	"productName":                  "API_MOCK_PROMPT_PRODUCT_NAME",
	"ResponseLanguage":             "API_MOCK_PROMPT_RESPONSE_LANGUAGE",
	"subAgentPrompt":               "API_MOCK_PROMPT_SUB_AGENT_PROMPT",
	"ToolResultPresentationPrompt": "API_MOCK_PROMPT_TOOL_RESULT_PRESENTATION_PROMPT",
	"UserLocalMemoryContent":       "API_MOCK_PROMPT_USER_LOCAL_MEMORY_CONTENT",
	"UserMemoryContent":            "API_MOCK_PROMPT_USER_MEMORY_CONTENT",
	"WorkingMemoryContent":         "API_MOCK_PROMPT_WORKING_MEMORY_CONTENT",
}

var modelInstructionTemplatePlaceholder = regexp.MustCompile(`\{\{\s*([A-Za-z][A-Za-z0-9_]*)\s*\}\}`)

func loadModelInstructions() (string, error) {
	text := prompts.ModelInstructions
	privateFile := false
	if path := strings.TrimSpace(os.Getenv("API_MOCK_MODEL_INSTRUCTIONS_FILE")); path != "" {
		body, err := os.ReadFile(path)
		if err != nil {
			return "", errors.New("API_MOCK_MODEL_INSTRUCTIONS_FILE could not be read")
		}
		text = string(body)
		text, err = renderModelInstructionTemplate(text)
		if err != nil {
			return "", err
		}
		privateFile = true
	}
	text = strings.TrimSpace(text)
	if len(text) < 100 {
		return "", errors.New("system instructions are missing or too short")
	}
	if !privateFile {
		if err := validateInjectedText("system instructions", text); err != nil {
			return "", err
		}
	}
	return text, nil
}

func renderModelInstructionTemplate(text string) (string, error) {
	var unknown string
	result := modelInstructionTemplatePlaceholder.ReplaceAllStringFunc(text, func(raw string) string {
		name := modelInstructionTemplatePlaceholder.FindStringSubmatch(raw)[1]
		key, ok := modelInstructionTemplateVariables[name]
		if !ok {
			unknown = name
			return raw
		}
		return os.Getenv(key)
	})
	if unknown != "" {
		return "", fmt.Errorf("system instructions contain unsupported template variable %q", unknown)
	}
	return result, nil
}

func loadWorkBuddyProfile() (map[string]string, error) {
	path := strings.TrimSpace(os.Getenv("API_MOCK_WORKBUDDY_PROFILE_FILE"))
	if path == "" {
		return nil, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("API_MOCK_WORKBUDDY_PROFILE_FILE could not be read")
	}
	var profile struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(body, &profile); err != nil || len(profile.Headers) == 0 {
		return nil, errors.New("API_MOCK_WORKBUDDY_PROFILE_FILE is invalid")
	}
	return profile.Headers, nil
}

func loadWorkBuddyTools() ([]any, error) {
	path := strings.TrimSpace(os.Getenv("API_MOCK_WORKBUDDY_TOOL_TEMPLATE_FILE"))
	if path == "" {
		return nil, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("API_MOCK_WORKBUDDY_TOOL_TEMPLATE_FILE could not be read")
	}
	var request map[string]any
	if json.Unmarshal(body, &request) != nil {
		return nil, errors.New("API_MOCK_WORKBUDDY_TOOL_TEMPLATE_FILE is invalid")
	}
	tools, ok := request["tools"].([]any)
	if !ok || len(tools) < 20 {
		return nil, errors.New("API_MOCK_WORKBUDDY_TOOL_TEMPLATE_FILE lacks a native tool catalog")
	}
	return tools, nil
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
	m.HandleFunc("GET /v1/models", a.models)
	m.HandleFunc("POST /v1/chat/completions", a.chat)
	m.HandleFunc("POST /api/login", a.login)
	m.HandleFunc("POST /api/logout", a.logout)
	m.HandleFunc("GET /api/session", a.session)
	m.HandleFunc("GET /api/diagnostics", a.guard(a.diagnostics))
	m.HandleFunc("GET /api/accounts", a.guard(a.accounts))
	m.HandleFunc("POST /api/accounts", a.guard(a.accounts))
	m.HandleFunc("DELETE /api/accounts/{id}", a.guard(a.account))
	m.HandleFunc("GET /api/records", a.guard(a.records))
	m.HandleFunc("POST /api/test", a.guard(a.testChat))
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
func (a *app) diagnostics(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	available := 0
	for _, account := range a.state.Accounts {
		if account.Enabled {
			available++
		}
	}
	jsonOut(w, 200, map[string]any{
		"ok":                  true,
		"accountsTotal":       len(a.state.Accounts),
		"accountsAvailable":   available,
		"records":             len(a.state.Records),
		"strategy":            a.state.Strategy,
		"configurationSource": "environment",
	})
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
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Label == "" || in.APIKey == "" || normalizeModelID(in.Model) == "" {
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
	a.state.Accounts = append(a.state.Accounts, Account{ID: randomString(9), Label: in.Label, Endpoint: endpoint, Model: normalizeModelID(in.Model), Enabled: in.Enabled, Key: key, CreatedAt: time.Now().UTC()})
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

func normalizeModelID(raw string) string {
	return strings.TrimSpace(raw)
}

func (a *app) candidateAccounts(model string) ([]Account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	available := make([]Account, 0)
	for _, account := range a.state.Accounts {
		if !account.Enabled || (model != "" && normalizeModelID(account.Model) != model) {
			continue
		}
		account.Model = normalizeModelID(account.Model)
		available = append(available, account)
	}
	if len(available) == 0 {
		if model != "" {
			return nil, errors.New("requested model is unavailable")
		}
		return nil, errors.New("account pool exhausted")
	}
	if a.state.Strategy == "round_robin" {
		start := a.state.Cursor % len(available)
		rotated := append(append([]Account{}, available[start:]...), available[:start]...)
		available = rotated
		a.state.Cursor++
		_ = a.save()
	}
	return available, nil
}

func (a *app) choose(model string) (Account, error) {
	accounts, err := a.candidateAccounts(model)
	if err != nil {
		return Account{}, err
	}
	return accounts[0], nil
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

func (a *app) models(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeAPI(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		appErr(w, 401, "invalid api key")
		return
	}
	a.mu.Lock()
	unique := make(map[string]struct{})
	for _, account := range a.state.Accounts {
		model := normalizeModelID(account.Model)
		if account.Enabled && model != "" {
			unique[model] = struct{}{}
		}
	}
	a.mu.Unlock()
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	data := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]any{"id": id, "object": "model", "created": 0, "owned_by": "api-mock"})
	}
	jsonOut(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (a *app) chat(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeAPI(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		appErr(w, 401, "invalid api key")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	body, e := io.ReadAll(r.Body)
	if e != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(e, &tooLarge) {
			jsonOut(w, http.StatusRequestEntityTooLarge, map[string]any{"error": map[string]any{"message": "request body exceeds 8 MiB limit", "type": "invalid_request_error", "code": "request_too_large"}})
			return
		}
		appErr(w, 400, "invalid request body")
		return
	}
	var request map[string]any
	if json.Unmarshal(body, &request) != nil || request["messages"] == nil {
		appErr(w, 400, "messages is required")
		return
	}
	model, ok := request["model"].(string)
	model = normalizeModelID(model)
	if !ok || model == "" {
		appErr(w, 400, "model is required")
		return
	}
	if _, ok := request["messages"].([]any); !ok {
		appErr(w, 400, "messages is required")
		return
	}
	selectionModel := model
	if isWorkBuddyConversationTopic(r) {
		selectionModel = ""
	}
	accounts, e := a.candidateAccounts(selectionModel)
	if e != nil {
		appErr(w, 503, e.Error())
		return
	}
	account := accounts[0]
	started := time.Now()
	if isWorkBuddyConversationTopic(r) {
		writeWorkBuddyConversationTopic(w, account.Model)
		a.record(account, request, requestResult{Outcome: outcomeSucceeded, HTTPStatus: http.StatusOK, Completed: true}, started)
		return
	}
	request["model"] = account.Model
	nativeWorkBuddy := isNativeWorkBuddyConversation(r, request)
	openAICompatible := strings.EqualFold(strings.TrimSpace(r.Header.Get("X-API-Mock-OpenAI-Compatible")), "1")
	workBuddyCompatible := isWorkBuddyCompatibleAgent(r)
	if workBuddyCompatible {
		applyAgentWorkBuddyProfile(request, a.modelInstructions, a.workBuddyTools)
	} else if openAICompatible {
		applyOpenAICompatibleProfile(request)
	} else if nativeWorkBuddy {
		applyNativeWorkBuddyProfile(request)
	} else {
		applyWorkBuddyRequestProfile(request, a.modelInstructions)
	}
	body, _ = json.Marshal(request)
	stream, _ := request["stream"].(bool)
	logicalRequestID := randomString(12)
	response, usedAccount, e := a.doUpstreamRequest(r, accounts, body, stream, openAICompatible, nativeWorkBuddy, workBuddyCompatible, logicalRequestID)
	account = usedAccount
	result := requestResult{Outcome: outcomeRequestFailed, FailureClass: classifyNetworkError(e)}
	if errors.Is(e, context.Canceled) {
		result.Outcome = outcomeClientCanceled
	} else if errors.Is(e, context.DeadlineExceeded) {
		result.Outcome = outcomeStreamIdleTimeout
	}
	if e == nil {
		result.HTTPStatus = response.StatusCode
		copySafeResponseHeaders(w.Header(), response.Header)
		isSSE := isSSEContentType(response.Header.Get("Content-Type"))
		if isSSE {
			w.Header().Set("Cache-Control", "no-cache, no-transform")
			w.Header().Set("X-Accel-Buffering", "no")
		} else if result.HTTPStatus < 400 {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.WriteHeader(response.StatusCode)
		detector := &sseErrorDetector{}
		if isSSE {
			e = copySSEWithHeartbeat(r.Context(), w, response.Body, detector)
		} else {
			_, e = io.Copy(w, response.Body)
		}
		response.Body.Close()
		result = classifyRequestResult(response.StatusCode, isSSE, e, detector)
		if e != nil && !errors.Is(e, context.Canceled) {
			log.Printf("upstream response interrupted: account_id=%s class=%s stream=%t", account.ID, result.FailureClass, isSSE)
		}
	} else {
		appErr(w, 502, "upstream request failed")
	}
	a.record(account, request, result, started)
}

func (a *app) testChat(w http.ResponseWriter, r *http.Request) {
	request := r.Clone(r.Context())
	request.Header = r.Header.Clone()
	request.Header.Set("Authorization", "Bearer "+a.apiKey)
	a.chat(w, request)
}

func isWorkBuddyCompatibleAgent(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-API-Mock-WorkBuddy-Compatible")), "1") ||
		strings.EqualFold(strings.TrimSpace(r.Header.Get("X-API-Mock-Pi-WorkBuddy")), "1")
}

func isWorkBuddyConversationTopic(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Agent-Purpose")), "conversation_topic")
}

func isNativeWorkBuddyConversation(r *http.Request, request map[string]any) bool {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Agent-Purpose")), "conversation") || strings.TrimSpace(r.Header.Get("X-CodeBuddy-Request")) != "1" {
		return false
	}
	if !strings.HasPrefix(strings.TrimSpace(r.Header.Get("User-Agent")), "WorkBuddy/") || !strings.EqualFold(strings.TrimSpace(r.Header.Get("X-IDE-Name")), "WorkBuddy") {
		return false
	}
	tools, ok := request["tools"].([]any)
	return ok && len(tools) >= 20
}

var workBuddyHeaderNames = []string{
	"Accept", "User-Agent", "X-Agent-Intent", "X-Agent-Purpose", "X-CodeBuddy-Request", "X-Domain", "X-IDE-Name", "X-IDE-Type", "X-IDE-Version", "X-Product", "X-Requested-With", "X-Stainless-Arch", "X-Stainless-Lang", "X-Stainless-OS", "X-Stainless-Package-Version", "X-Stainless-Retry-Count", "X-Stainless-Runtime", "X-Stainless-Runtime-Version", "Acp-Connection-ID", "B3", "Traceparent", "X-B3-ParentSpanID", "X-B3-Sampled", "X-B3-SpanID", "X-B3-TraceID", "X-Trace-ID", "X-User-ID", "X-Conversation-ID", "X-Conversation-Message-ID", "X-Conversation-Request-ID", "X-Request-ID",
}

func overlayNativeWorkBuddyHeaders(target map[string]string, source http.Header) {
	profile := make(map[string]string, len(workBuddyHeaderNames))
	for _, name := range workBuddyHeaderNames {
		profile[name] = source.Get(name)
	}
	overlayWorkBuddyProfile(target, profile)
}

func overlayWorkBuddyProfile(target, source map[string]string) {
	for _, name := range workBuddyHeaderNames {
		if value := strings.TrimSpace(workBuddyProfileHeader(source, name)); value != "" {
			target[name] = value
		}
	}
}

func workBuddyProfileHeader(profile map[string]string, name string) string {
	if value := profile[name]; value != "" {
		return value
	}
	canonical := http.CanonicalHeaderKey(name)
	if value := profile[canonical]; value != "" {
		return value
	}
	for key, value := range profile {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func applyNativeWorkBuddyProfile(request map[string]any) {
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

func classifyRequestResult(status int, isSSE bool, err error, detector *sseErrorDetector) requestResult {
	result := requestResult{HTTPStatus: status}
	switch {
	case status >= http.StatusBadRequest:
		result.Outcome = outcomeUpstreamHTTPError
		result.FailureClass = "http_status"
	case detector != nil && detector.failed:
		result.Outcome = outcomeUpstreamSSEError
		result.FailureClass = "sse_error"
	case errors.Is(err, context.Canceled):
		result.Outcome = outcomeClientCanceled
		result.FailureClass = "client_cancel"
	case errors.Is(err, context.DeadlineExceeded):
		result.Outcome = outcomeStreamIdleTimeout
		result.FailureClass = "timeout"
	case err != nil:
		result.Outcome = outcomeUpstreamStreamInterrupted
		result.FailureClass = classifyNetworkError(err)
	case isSSE && (detector == nil || !detector.sawDone):
		result.Outcome = outcomeUpstreamStreamInterrupted
		result.FailureClass = "incomplete_sse"
	default:
		result.Outcome = outcomeSucceeded
		result.Completed = true
	}
	if isSSE && detector != nil && detector.sawDone && result.Outcome == outcomeSucceeded {
		result.Completed = true
	}
	return result
}

func legacyStatus(outcome string) string {
	if outcome == outcomeSucceeded {
		return "succeeded"
	}
	if outcome == outcomeClientCanceled {
		return "canceled"
	}
	return "failed"
}

func (a *app) record(account Account, request map[string]any, result requestResult, started time.Time) {
	a.mu.Lock()
	for i := range a.state.Accounts {
		if a.state.Accounts[i].ID == account.ID {
			a.state.Accounts[i].LastUsedAt = time.Now().UTC()
		}
	}
	a.state.Records = append([]Record{{ID: randomString(8), AccountID: account.ID, AccountLabel: account.Label, Model: account.Model, Status: legacyStatus(result.Outcome), Outcome: result.Outcome, FailureClass: result.FailureClass, HTTPStatus: result.HTTPStatus, Stream: request["stream"] == true, Completed: result.Completed, DurationMS: time.Since(started).Milliseconds(), At: time.Now().UTC()}}, a.state.Records...)
	if len(a.state.Records) > 100 {
		a.state.Records = a.state.Records[:100]
	}
	_ = a.save()
	a.mu.Unlock()
}

func (a *app) captureOutgoing(body []byte, headers map[string]string, logicalRequestID string, attempt int) {
	if a.outgoingCaptureDir == "" {
		return
	}
	redactedBody, err := redactCaptureBody(body)
	if err != nil {
		return
	}
	if os.MkdirAll(a.outgoingCaptureDir, 0o700) != nil {
		return
	}
	stem := time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + randomString(8)
	profile := make(map[string]string, len(headers))
	for name, value := range headers {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "x-api-key" || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") {
			continue
		}
		if isCaptureIdentifierField(lower) {
			profile[name] = "[REDACTED]"
			continue
		}
		profile[name] = value
	}
	encoded, err := json.Marshal(map[string]any{"headers": profile, "request_id": logicalRequestID, "attempt": attempt})
	if err != nil {
		return
	}
	if os.WriteFile(filepath.Join(a.outgoingCaptureDir, stem+".json"), redactedBody, 0o600) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(a.outgoingCaptureDir, stem+".profile.json"), encoded, 0o600)
}

func redactCaptureBody(body []byte) ([]byte, error) {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	redactCaptureValue(payload)
	return json.Marshal(payload)
}

func redactCaptureValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for name, child := range typed {
			lower := strings.ToLower(strings.ReplaceAll(name, "-", "_"))
			if isCaptureSecretField(lower) || isCaptureContentField(lower) || isCaptureIdentifierField(lower) {
				typed[name] = "[REDACTED]"
				continue
			}
			redactCaptureValue(child)
		}
	case []any:
		for _, child := range typed {
			redactCaptureValue(child)
		}
	}
}

func isCaptureSecretField(name string) bool {
	return name == "authorization" || name == "api_key" || name == "apikey" || name == "password" ||
		strings.Contains(name, "token") || strings.Contains(name, "secret")
}

func isCaptureContentField(name string) bool {
	return name == "content" || name == "text" || name == "arguments" || name == "input" || name == "prompt" ||
		name == "url" || name == "image_url"
}

func isCaptureIdentifierField(name string) bool {
	name = strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	return name == "id" || name == "user" || name == "traceparent" || name == "b3" ||
		strings.HasSuffix(name, "user_id") || strings.HasSuffix(name, "request_id") ||
		strings.HasSuffix(name, "conversation_id") || strings.HasSuffix(name, "message_id") ||
		strings.HasSuffix(name, "tool_call_id") || strings.HasSuffix(name, "connection_id") ||
		strings.HasSuffix(name, "trace_id") || strings.HasSuffix(name, "span_id")
}

const maxSSEInspectionLineBytes = 64 << 10

type sseErrorDetector struct {
	line    []byte
	failed  bool
	sawDone bool
}

func (d *sseErrorDetector) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			d.inspectLine()
			d.line = d.line[:0]
			continue
		}
		if len(d.line) < maxSSEInspectionLineBytes {
			d.line = append(d.line, b)
		}
	}
	return len(p), nil
}

func (d *sseErrorDetector) inspectLine() {
	line := strings.TrimSpace(string(d.line))
	if strings.EqualFold(line, "event: error") {
		d.failed = true
		return
	}
	if !strings.HasPrefix(line, "data:") {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "[DONE]" {
		d.sawDone = true
		return
	}
	var value map[string]any
	if json.Unmarshal([]byte(payload), &value) == nil {
		if _, ok := value["error"]; ok {
			d.failed = true
		}
	}
}

func applyWorkBuddyRequestProfile(request map[string]any, modelInstructions string) {
	prependModelInstructions(request, modelInstructions)
	stream, _ := request["stream"].(bool)
	request["stream"] = stream
	if _, ok := request["reasoning_effort"]; !ok {
		request["reasoning_effort"] = "low"
	}
	request["temperature"] = 1
	if stream {
		request["stream_options"] = map[string]any{"include_usage": true}
	} else {
		delete(request, "stream_options")
	}
}

func applyOpenAICompatibleProfile(request map[string]any) {
	if maxTokens, ok := request["max_completion_tokens"]; ok {
		request["max_tokens"] = maxTokens
	}
	for _, field := range []string{"max_completion_tokens", "reasoning_effort", "store", "stream_options", "tools", "tool_choice"} {
		delete(request, field)
	}
	request["stream"] = false
	request["temperature"] = 0
	messages, _ := request["messages"].([]any)
	normalized := make([]any, 0, len(messages))
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if message["role"] == "developer" {
			continue
		}
		parts, typedContent := message["content"].([]any)
		if typedContent {
			var text strings.Builder
			for _, rawPart := range parts {
				part, ok := rawPart.(map[string]any)
				if !ok || part["type"] != "text" {
					continue
				}
				if value, ok := part["text"].(string); ok {
					text.WriteString(value)
				}
			}
			message["content"] = text.String()
		}
		normalized = append(normalized, message)
	}
	request["messages"] = normalized
}

func applyAgentWorkBuddyProfile(request map[string]any, modelInstructions string, workBuddyTools []any) {
	if _, hasCompletionCap := request["max_completion_tokens"]; hasCompletionCap {
		delete(request, "max_tokens")
	}
	delete(request, "store")
	if messages, ok := request["messages"].([]any); ok {
		filtered := make([]any, 0, len(messages))
		for _, raw := range messages {
			message, ok := raw.(map[string]any)
			if ok && (message["role"] == "system" || message["role"] == "developer") {
				continue
			}
			filtered = append(filtered, raw)
		}
		request["messages"] = filtered
	}
	if _, hasTools := request["tools"].([]any); !hasTools && len(workBuddyTools) > 0 {
		request["tools"] = workBuddyTools
		delete(request, "tool_choice")
	}
	applyWorkBuddyRequestProfile(request, modelInstructions)
}

func prependModelInstructions(request map[string]any, instructions string) {
	messages, ok := request["messages"].([]any)
	if !ok || instructions == "" {
		return
	}
	if len(messages) > 0 && isModelInstructions(messages[0], instructions) {
		return
	}
	request["messages"] = append([]any{map[string]any{
		"role":    "system",
		"content": instructions,
	}}, messages...)
}

func isModelInstructions(raw any, instructions string) bool {
	message, ok := raw.(map[string]any)
	if !ok || message["role"] != "system" {
		return false
	}
	if content, ok := message["content"].(string); ok {
		return content == instructions
	}
	content, ok := message["content"].([]any)
	if !ok || len(content) != 1 {
		return false
	}
	part, ok := content[0].(map[string]any)
	return ok && part["type"] == "text" && part["text"] == instructions
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
