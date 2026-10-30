package infer

import (
	"bytes"
	"context"
	"net/http"
	"time"
)

// hostWake asks the RecNonstopGpu manager to load services on demand. It is
// best-effort: an older manager or a disabled manager must not break ASR.
func hostWake(ctx context.Context, url, token string) {
	if url == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Api-Token", token)
	}
	hc := &http.Client{Timeout: 60 * time.Second}
	resp, err := hc.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

// hostKeepAlive keeps the manager's idle timer from releasing services while a
// long transcription is running.
func hostKeepAlive(ctx context.Context, touchURL, token string) func() {
	if touchURL == "" {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, touchURL, bytes.NewReader([]byte("{}")))
				if err != nil {
					continue
				}
				req.Header.Set("Content-Type", "application/json")
				if token != "" {
					req.Header.Set("X-Api-Token", token)
				}
				hc := &http.Client{Timeout: 5 * time.Second}
				if resp, err := hc.Do(req); err == nil {
					_ = resp.Body.Close()
				}
			}
		}
	}()
	return func() { close(done) }
}
