package apimock

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

type streamRead struct {
	data []byte
	err  error
}

func copySSEWithHeartbeat(ctx context.Context, w http.ResponseWriter, body io.Reader, detector *sseErrorDetector) error {
	return copySSE(ctx, w, body, detector, sseHeartbeatInterval, streamIdleTimeout)
}

func copySSE(ctx context.Context, w http.ResponseWriter, body io.Reader, detector *sseErrorDetector, heartbeatInterval, idleTimeout time.Duration) error {
	flusher, _ := w.(http.Flusher)
	reads := make(chan streamRead, 1)
	go func() {
		buffer := make([]byte, 32<<10)
		for {
			n, err := body.Read(buffer)
			chunk := append([]byte(nil), buffer[:n]...)
			select {
			case reads <- streamRead{data: chunk, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	idle := time.NewTimer(idleTimeout)
	defer idle.Stop()
	resetIdle := func() {
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(idleTimeout)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idle.C:
			return context.DeadlineExceeded
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		case result := <-reads:
			if len(result.data) > 0 {
				_, _ = detector.Write(result.data)
				if _, err := w.Write(result.data); err != nil {
					return err
				}
				if flusher != nil {
					flusher.Flush()
				}
				resetIdle()
			}
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return nil
				}
				return result.err
			}
		}
	}
}
