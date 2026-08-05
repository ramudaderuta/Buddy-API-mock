package apimock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
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
	maxSafeAttempts            = 10
	maxSafeRetryDelay          = 60 * time.Second
)

var safeRetryBackoff = [...]time.Duration{
	250 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	32 * time.Second,
	60 * time.Second,
}

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

func (a *app) doUpstreamRequest(r *http.Request, accounts []Account, body []byte, stream, openAICompatible, nativeWorkBuddy, workBuddyCompatible bool, logicalRequestID string) (*http.Response, Account, *outgoingCapture, error) {
	if len(accounts) == 0 {
		return nil, Account{}, nil, errors.New("no upstream accounts available")
	}
	used := accounts[0]
	var capture *outgoingCapture
	for attempt := 0; attempt < maxSafeAttempts; attempt++ {
		key, err := a.decrypt(used.Key)
		if err != nil {
			return nil, used, capture, errors.New("account key unavailable")
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
			return nil, used, capture, err
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
			capture = a.captureOutgoing(body, headers, logicalRequestID, attempt+1)
		}
		response, err := a.client.Do(upstream)
		if response != nil {
			response.Body = &cancelOnClose{ReadCloser: response.Body, cancel: cancel}
			return response, used, capture, nil
		}
		cancel()
		if err == nil || r.Context().Err() != nil || !trace.safeToRetry() {
			return nil, used, capture, err
		}
		class := classifyNetworkError(err)
		if attempt+1 >= maxSafeAttempts {
			log.Printf("upstream pre-write failure: account_id=%s class=%s attempt=%d retry_exhausted=true", used.ID, class, attempt+1)
			return nil, used, capture, err
		}
		delay := retryDelay(attempt)
		log.Printf("upstream pre-write failure: account_id=%s class=%s attempt=%d retry_in_ms=%d", used.ID, class, attempt+1, delay.Milliseconds())
		if !a.waitForRetry(r.Context(), delay) {
			if canceled := r.Context().Err(); canceled != nil {
				return nil, used, capture, canceled
			}
			return nil, used, capture, context.Canceled
		}
	}
	return nil, used, capture, errors.New("safe retry attempts exhausted")
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

func retryDelay(accountFailure int) time.Duration {
	if accountFailure < 0 {
		return safeRetryBackoff[0]
	}
	if accountFailure >= len(safeRetryBackoff) {
		return maxSafeRetryDelay
	}
	return safeRetryBackoff[accountFailure]
}

func (a *app) waitForRetry(ctx context.Context, delay time.Duration) bool {
	if a.retryWait != nil {
		return a.retryWait(ctx, delay)
	}
	return waitForRetry(ctx, delay)
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
