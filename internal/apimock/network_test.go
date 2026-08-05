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

func TestSafeRetryStopsAfterTenAttempts(t *testing.T) {
	a := &app{key: make([]byte, 32)}
	accounts := []Account{
		encryptedTestAccount(t, a, "first", "https://first.example/v1", "model-a", "key-first"),
		encryptedTestAccount(t, a, "second", "https://second.example/v1", "model-a", "key-second"),
	}
	var hosts []string
	var delays []time.Duration
	a.retryWait = func(_ context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		return true
	}
	a.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		hosts = append(hosts, request.URL.Host)
		return nil, &net.DNSError{Err: "temporary lookup failure", Name: request.URL.Host}
	})}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	_, used, _, err := a.doUpstreamRequest(request, accounts, []byte(`{"model":"model-a"}`), false, false, false, false, "request-test")
	if err == nil {
		t.Fatal("expected retry exhaustion error")
	}
	if len(hosts) != maxSafeAttempts || used.ID != "first" {
		t.Fatalf("hosts=%#v used=%q", hosts, used.ID)
	}
	for i, host := range hosts {
		if host != "first.example" {
			t.Fatalf("attempt %d unexpectedly switched to %q", i+1, host)
		}
	}
	if len(delays) != maxSafeAttempts-1 {
		t.Fatalf("delays=%#v", delays)
	}
	for i, want := range safeRetryBackoff[:maxSafeAttempts-1] {
		if delays[i] != want {
			t.Fatalf("delay[%d]=%v want %v", i, delays[i], want)
		}
	}
}

func TestSafeRetryCanSucceedOnTenthAttempt(t *testing.T) {
	a := &app{key: make([]byte, 32)}
	accounts := []Account{encryptedTestAccount(t, a, "only", "https://only.example/v1", "model-a", "key")}
	var delays []time.Duration
	a.retryWait = func(_ context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		return true
	}
	attempts := 0
	a.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		if attempts < maxSafeAttempts {
			return nil, &net.DNSError{Err: "temporary lookup failure", Name: request.URL.Host}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
	})}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	response, _, _, err := a.doUpstreamRequest(request, accounts, []byte(`{"model":"model-a"}`), false, false, false, false, "request-test")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if attempts != maxSafeAttempts || len(delays) != maxSafeAttempts-1 {
		t.Fatalf("attempts=%d delays=%#v", attempts, delays)
	}
	if delays[len(delays)-1] != maxSafeRetryDelay {
		t.Fatalf("last delay=%v", delays[len(delays)-1])
	}
}

func TestSafeRetryNeverReplaysAfterRequestWrite(t *testing.T) {
	a := &app{key: make([]byte, 32)}
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
	_, used, _, err := a.doUpstreamRequest(request, accounts, []byte(`{"model":"model-a"}`), false, false, false, false, "request-test")
	if !errors.Is(err, io.ErrUnexpectedEOF) || attempts != 1 || used.ID != "first" {
		t.Fatalf("error=%v attempts=%d used=%q", err, attempts, used.ID)
	}
}

func TestHTTP503IsReturnedWithoutRelayRetry(t *testing.T) {
	a := &app{key: make([]byte, 32)}
	accounts := []Account{
		encryptedTestAccount(t, a, "first", "https://first.example/v1", "model-a", "key-first"),
		encryptedTestAccount(t, a, "second", "https://second.example/v1", "model-a", "key-second"),
	}
	attempts := 0
	a.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Retry-After": {"5"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"busy"}}`)), Request: request}, nil
	})}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	response, used, _, err := a.doUpstreamRequest(request, accounts, []byte(`{"model":"model-a"}`), false, false, false, false, "request-test")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if attempts != 1 || used.ID != "first" || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("attempts=%d used=%q status=%d", attempts, used.ID, response.StatusCode)
	}
}

func TestClientCancellationStopsRetryQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &app{key: make([]byte, 32)}
	a.retryWait = func(_ context.Context, _ time.Duration) bool {
		cancel()
		return false
	}
	accounts := []Account{encryptedTestAccount(t, a, "first", "https://first.example/v1", "model-a", "key-first")}
	attempts := 0
	a.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		return nil, &net.DNSError{Err: "temporary lookup failure", Name: request.URL.Host}
	})}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	_, _, _, err := a.doUpstreamRequest(request, accounts, []byte(`{"model":"model-a"}`), false, false, false, false, "request-test")
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("error=%v attempts=%d", err, attempts)
	}
}

func TestCandidateAccountsNeverSuspendsEnabledAccounts(t *testing.T) {
	a := &app{state: state{Accounts: []Account{
		{ID: "first", Model: "model-a", Enabled: true},
		{ID: "second", Model: "model-a", Enabled: true},
	}}}
	accounts, err := a.candidateAccounts("model-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].ID != "first" || accounts[1].ID != "second" {
		t.Fatalf("available accounts=%#v", accounts)
	}
}

func TestRetryDelayCapsAtSixtySeconds(t *testing.T) {
	for i, want := range safeRetryBackoff {
		if got := retryDelay(i); got != want {
			t.Fatalf("retryDelay(%d)=%v want %v", i, got, want)
		}
	}
	if got := retryDelay(100); got != maxSafeRetryDelay {
		t.Fatalf("capped delay=%v", got)
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

func TestClassifyRequestResultSeparatesStreamingOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		err       error
		detector  *sseErrorDetector
		outcome   string
		completed bool
	}{
		{name: "complete", status: 200, detector: &sseErrorDetector{sawDone: true}, outcome: outcomeSucceeded, completed: true},
		{name: "client canceled", status: 200, err: context.Canceled, detector: &sseErrorDetector{}, outcome: outcomeClientCanceled},
		{name: "sse error", status: 200, detector: &sseErrorDetector{failed: true}, outcome: outcomeUpstreamSSEError},
		{name: "incomplete eof", status: 200, detector: &sseErrorDetector{}, outcome: outcomeUpstreamStreamInterrupted},
		{name: "idle timeout", status: 200, err: context.DeadlineExceeded, detector: &sseErrorDetector{}, outcome: outcomeStreamIdleTimeout},
		{name: "http error", status: 503, detector: &sseErrorDetector{}, outcome: outcomeUpstreamHTTPError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyRequestResult(tt.status, true, tt.err, tt.detector)
			if result.Outcome != tt.outcome || result.Completed != tt.completed {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}
