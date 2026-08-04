package apimock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"
)

const (
	maxRequestBodyBytes        = 8 << 20
	connectTimeout             = 10 * time.Second
	tcpKeepAlive               = 30 * time.Second
	tlsHandshakeTimeout        = 10 * time.Second
	responseHeaderTimeout      = 180 * time.Second
	expectContinueTimeout      = 2 * time.Second
	idleConnectionTimeout      = 90 * time.Second
	nonStreamingRequestTimeout = 10 * time.Minute
	streamIdleTimeout          = 180 * time.Second
	sseHeartbeatInterval       = 20 * time.Second
	serverReadHeaderTimeout    = 10 * time.Second
	serverIdleTimeout          = 120 * time.Second
	accountCooldownDuration    = 60 * time.Second
	accountFailureThreshold    = 3
	maxSafeAttempts            = 3
)

var safeRetryBackoff = [...]time.Duration{250 * time.Millisecond, 750 * time.Millisecond}

func newUpstreamHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		DialContext:           (&net.Dialer{Timeout: connectTimeout, KeepAlive: tcpKeepAlive}).DialContext,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ExpectContinueTimeout: expectContinueTimeout,
		IdleConnTimeout:       idleConnectionTimeout,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		MaxConnsPerHost:       50,
	}}
}

func (a *app) doUpstreamRequest(r *http.Request, accounts []Account, body []byte, stream, openAICompatible, nativeWorkBuddy, workBuddyCompatible bool, logicalRequestID string) (*http.Response, Account, error) {
	if len(accounts) == 0 {
		return nil, Account{}, errors.New("no upstream accounts available")
	}
	var lastErr error
	var used Account
	for attempt := 0; attempt < maxSafeAttempts; attempt++ {
		selected := false
		for offset := 0; offset < len(accounts); offset++ {
			candidate := accounts[(attempt+offset)%len(accounts)]
			if !a.accountInCooldown(candidate.ID, time.Now()) {
				used = candidate
				selected = true
				break
			}
		}
		if !selected {
			break
		}
		key, err := a.decrypt(used.Key)
		if err != nil {
			return nil, used, errors.New("account key unavailable")
		}
		ctx := r.Context()
		cancel := func() {}
		if !stream {
			ctx, cancel = context.WithTimeout(ctx, nonStreamingRequestTimeout)
		}
		trace := &attemptTrace{}
		ctx = httptrace.WithClientTrace(ctx, trace.clientTrace())
		upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, used.Endpoint+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			cancel()
			return nil, used, err
		}
		headers := workBuddyHeaders(key, a.workBuddyUserID)
		if openAICompatible {
			headers = map[string]string{"Authorization": "Bearer " + key, "User-Agent": "curl/8.5.0", "Accept": "*/*"}
		} else if nativeWorkBuddy {
			overlayNativeWorkBuddyHeaders(headers, r.Header)
		} else {
			overlayWorkBuddyProfile(headers, a.workBuddyProfile)
		}
		for name, value := range headers {
			upstream.Header.Set(name, value)
		}
		upstream.Header.Set("Content-Type", "application/json")
		if workBuddyCompatible {
			a.captureOutgoing(body, headers, logicalRequestID, attempt+1)
		}
		started := time.Now()
		response, err := a.client.Do(upstream)
		latency := time.Since(started)
		if response != nil {
			response.Body = &cancelOnClose{ReadCloser: response.Body, cancel: cancel}
			return response, used, nil
		}
		cancel()
		lastErr = err
		if err == nil || r.Context().Err() != nil || !trace.safeToRetry() {
			return nil, used, err
		}
		class := classifyNetworkError(err)
		a.markAccountFailure(used, class, latency)
		delay := retryDelay(attempt)
		log.Printf("upstream pre-write failure: account_id=%s class=%s attempt=%d retry_in_ms=%d", used.ID, class, attempt+1, delay.Milliseconds())
		if attempt+1 >= maxSafeAttempts || !waitForRetry(r.Context(), delay) {
			break
		}
	}
	return nil, used, lastErr
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *cancelOnClose) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

func (a *app) accountInCooldown(accountID string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	health := a.health[accountID]
	return health != nil && health.CooldownUntil.After(now)
}

type accountHealth struct {
	ConsecutiveFailures int
	CooldownUntil       time.Time
	LastSuccessAt       time.Time
	LastErrorClass      string
	LastLatency         time.Duration
}

type attemptTrace struct {
	mu          sync.Mutex
	wrote       bool
	writeErr    error
	gotResponse bool
}

func (t *attemptTrace) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			t.mu.Lock()
			t.wrote = true
			t.writeErr = info.Err
			t.mu.Unlock()
		},
		GotFirstResponseByte: func() {
			t.mu.Lock()
			t.gotResponse = true
			t.mu.Unlock()
		},
	}
}

func (t *attemptTrace) safeToRetry() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.wrote && !t.gotResponse
}

func retryDelay(attempt int) time.Duration {
	if attempt < 0 || attempt >= len(safeRetryBackoff) {
		return 0
	}
	base := safeRetryBackoff[attempt]
	jitter := time.Duration(rand.Int64N(int64(base/5 + 1)))
	return base + jitter
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func classifyNetworkError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "client_cancel"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "network"
	}
	return "transport"
}

var forwardedResponseHeaders = []string{
	"Content-Type",
	"Cache-Control",
	"X-Request-Id",
	"OpenAI-Processing-Ms",
	"Retry-After",
	"RateLimit-Limit",
	"RateLimit-Remaining",
	"RateLimit-Reset",
	"X-RateLimit-Limit-Requests",
	"X-RateLimit-Limit-Tokens",
	"X-RateLimit-Remaining-Requests",
	"X-RateLimit-Remaining-Tokens",
	"X-RateLimit-Reset-Requests",
	"X-RateLimit-Reset-Tokens",
	"CF-Ray",
}

func copySafeResponseHeaders(dst, src http.Header) {
	for _, name := range forwardedResponseHeaders {
		if values := src.Values(name); len(values) > 0 {
			dst.Del(name)
			for _, value := range values {
				dst.Add(name, value)
			}
		}
	}
}

func isSSEContentType(value string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	return mediaType == "text/event-stream"
}
