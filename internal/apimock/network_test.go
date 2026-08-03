package apimock

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func encryptedTestAccount(t *testing.T, a *app, id, endpoint, model, key string) Account {
	t.Helper()
	encrypted, err := a.encrypt(key)
	if err != nil {
		t.Fatal(err)
	}
	return Account{ID: id, Endpoint: endpoint, Model: model, Enabled: true, Key: encrypted}
}

func TestNewUpstreamHTTPClientUsesFixedTransportPolicy(t *testing.T) {
	client := newUpstreamHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", client.Transport)
	}
	if client.Timeout != 0 {
		t.Fatalf("whole-request timeout = %v, want 0", client.Timeout)
	}
	if transport.TLSHandshakeTimeout != tlsHandshakeTimeout ||
		transport.ResponseHeaderTimeout != responseHeaderTimeout ||
		transport.ExpectContinueTimeout != expectContinueTimeout ||
		transport.IdleConnTimeout != idleConnectionTimeout ||
		transport.MaxIdleConns != 100 || transport.MaxIdleConnsPerHost != 20 ||
		transport.MaxConnsPerHost != 50 || !transport.ForceAttemptHTTP2 {
		t.Fatalf("unexpected transport: %#v", transport)
	}
}

func TestCopySafeResponseHeadersAllowsDiagnosticsAndRejectsCookies(t *testing.T) {
	source := http.Header{
		"Content-Type":                   {"application/json"},
		"Retry-After":                    {"3"},
		"X-Ratelimit-Remaining-Requests": {"7"},
		"Cf-Ray":                         {"example-ray"},
		"Set-Cookie":                     {"private=secret"},
		"Connection":                     {"close"},
	}
	target := http.Header{}
	copySafeResponseHeaders(target, source)
	for _, name := range []string{"Content-Type", "Retry-After", "X-RateLimit-Remaining-Requests", "CF-Ray"} {
		if target.Get(name) == "" {
			t.Fatalf("missing forwarded header %s: %#v", name, target)
		}
	}
	if target.Get("Set-Cookie") != "" || target.Get("Connection") != "" {
		t.Fatalf("unsafe header forwarded: %#v", target)
	}
}

func TestSafeRetrySwitchesAccountsOnlyBeforeRequestWrite(t *testing.T) {
	a := &app{key: make([]byte, 32), health: map[string]*accountHealth{}}
	accounts := []Account{
		encryptedTestAccount(t, a, "first", "https://first.example/v1", "model-a", "key-first"),
		encryptedTestAccount(t, a, "second", "https://second.example/v1", "model-a", "key-second"),
	}
	var mu sync.Mutex
	attempts := 0
	var secondAuthorization string
	a.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts == 1 {
			return nil, &net.DNSError{Err: "temporary lookup failure", Name: request.URL.Host}
		}
		secondAuthorization = request.Header.Get("Authorization")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[]}`)),
			Request:    request,
		}, nil
	})}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	response, used, err := a.doUpstreamRequest(request, accounts, []byte(`{"model":"model-a"}`), false, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if attempts != 2 || used.ID != "second" || secondAuthorization != "Bearer key-second" {
		t.Fatalf("attempts=%d used=%q auth=%q", attempts, used.ID, secondAuthorization)
	}
	if a.health["first"].ConsecutiveFailures != 1 {
		t.Fatalf("first account health = %#v", a.health["first"])
	}
	if a.health["second"].ConsecutiveFailures != 0 || a.health["second"].LastSuccessAt.IsZero() {
		t.Fatalf("second account health = %#v", a.health["second"])
	}
}

func TestSafeRetryNeverReplaysAfterRequestWrite(t *testing.T) {
	a := &app{key: make([]byte, 32), health: map[string]*accountHealth{}}
	accounts := []Account{
		encryptedTestAccount(t, a, "first", "https://first.example/v1", "model-a", "key-first"),
		encryptedTestAccount(t, a, "second", "https://second.example/v1", "model-a", "key-second"),
	}
	attempts := 0
	a.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		if trace := httptrace.ContextClientTrace(request.Context()); trace != nil && trace.WroteRequest != nil {
			trace.WroteRequest(httptrace.WroteRequestInfo{})
		}
		return nil, io.ErrUnexpectedEOF
	})}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	_, used, err := a.doUpstreamRequest(request, accounts, []byte(`{"model":"model-a"}`), false, false, false, false)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v", err)
	}
	if attempts != 1 || used.ID != "first" {
		t.Fatalf("attempts=%d used=%q", attempts, used.ID)
	}
	if a.health["first"] != nil {
		t.Fatalf("ambiguous post-write failure must not enter safe-failure cooldown: %#v", a.health["first"])
	}
}

func TestHTTP503IsReturnedWithoutRetryOrHealthReset(t *testing.T) {
	a := &app{key: make([]byte, 32), health: map[string]*accountHealth{"first": {ConsecutiveFailures: 2}}}
	accounts := []Account{
		encryptedTestAccount(t, a, "first", "https://first.example/v1", "model-a", "key-first"),
		encryptedTestAccount(t, a, "second", "https://second.example/v1", "model-a", "key-second"),
	}
	attempts := 0
	a.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Retry-After": {"5"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"busy"}}`)),
			Request:    request,
		}, nil
	})}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	response, used, err := a.doUpstreamRequest(request, accounts, []byte(`{"model":"model-a"}`), false, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if attempts != 1 || used.ID != "first" || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("attempts=%d used=%q status=%d", attempts, used.ID, response.StatusCode)
	}
	if a.health["first"].ConsecutiveFailures != 2 {
		t.Fatalf("5xx response must not reset safe-network failure state: %#v", a.health["first"])
	}
}

func TestClientCancellationNeverRetries(t *testing.T) {
	a := &app{key: make([]byte, 32), health: map[string]*accountHealth{}}
	accounts := []Account{encryptedTestAccount(t, a, "first", "https://first.example/v1", "model-a", "key-first")}
	attempts := 0
	a.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		return nil, context.Canceled
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	_, _, err := a.doUpstreamRequest(request, accounts, []byte(`{"model":"model-a"}`), false, false, false, false)
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("error=%v attempts=%d", err, attempts)
	}
	if a.health["first"] != nil {
		t.Fatalf("client cancellation must not affect account health: %#v", a.health["first"])
	}
}

func TestSafeRetryRechecksCooldownWithinSameRequest(t *testing.T) {
	a := &app{key: make([]byte, 32), health: map[string]*accountHealth{"first": {ConsecutiveFailures: accountFailureThreshold - 1}}}
	accounts := []Account{
		encryptedTestAccount(t, a, "first", "https://first.example/v1", "model-a", "key-first"),
		encryptedTestAccount(t, a, "second", "https://second.example/v1", "model-a", "key-second"),
	}
	var hosts []string
	a.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		hosts = append(hosts, request.URL.Host)
		if len(hosts) == 1 {
			return nil, &net.DNSError{Err: "temporary lookup failure", Name: request.URL.Host}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"choices":[]}`)),
			Request:    request,
		}, nil
	})}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	response, used, err := a.doUpstreamRequest(request, accounts, []byte(`{"model":"model-a"}`), false, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if len(hosts) != 2 || hosts[0] != "first.example" || hosts[1] != "second.example" || used.ID != "second" {
		t.Fatalf("hosts=%#v used=%q", hosts, used.ID)
	}
	if !a.health["first"].CooldownUntil.After(time.Now()) {
		t.Fatalf("first account was not cooled down: %#v", a.health["first"])
	}
}

func TestAccountCooldownExcludesRepeatedSafeFailures(t *testing.T) {
	a := &app{health: map[string]*accountHealth{}, state: state{Accounts: []Account{
		{ID: "first", Model: "model-a", Enabled: true},
		{ID: "second", Model: "model-a", Enabled: true},
	}}}
	for range accountFailureThreshold {
		a.markAccountFailure(a.state.Accounts[0], "connect", time.Second)
	}
	accounts, err := a.candidateAccounts("model-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != "second" {
		t.Fatalf("available accounts = %#v", accounts)
	}
	if !a.health["first"].CooldownUntil.After(time.Now()) {
		t.Fatalf("cooldown not set: %#v", a.health["first"])
	}
}

func TestCopySSEEmitsHeartbeatAndFlushesData(t *testing.T) {
	reader, writer := io.Pipe()
	response := httptest.NewRecorder()
	detector := &sseErrorDetector{}
	done := make(chan error, 1)
	go func() {
		done <- copySSE(context.Background(), response, reader, detector, 5*time.Millisecond, 100*time.Millisecond)
	}()
	time.Sleep(12 * time.Millisecond)
	_, _ = writer.Write([]byte("data: {\"choices\":[]}\n\n"))
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	body := response.Body.String()
	if !strings.Contains(body, ": ping\n\n") || !strings.Contains(body, "data: {\"choices\":[]}") {
		t.Fatalf("stream body = %q", body)
	}
	if !response.Flushed {
		t.Fatal("expected streaming response to flush")
	}
}

func TestCopySSEStopsAfterIdleTimeout(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	response := httptest.NewRecorder()
	err := copySSE(context.Background(), response, reader, &sseErrorDetector{}, time.Second, 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}
