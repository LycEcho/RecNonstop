package infer

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// InferenceGate bounds model requests shared by otherwise independent ASR and
// text clients. It limits requests from this process, not resident model memory
// or requests sent directly by other clients. Non-positive limits default to 1.
type InferenceGate struct {
	slots chan struct{}
}

func NewInferenceGate(maxConcurrent int) *InferenceGate {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &InferenceGate{slots: make(chan struct{}, maxConcurrent)}
}

// Transport wraps an existing transport, using http.DefaultTransport when nil.
// Only inference POSTs enter the gate; health/status requests remain responsive.
func (g *InferenceGate) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &inferenceTransport{gate: g, base: base}
}

type inferenceTransport struct {
	gate *InferenceGate
	base http.RoundTripper
}

func isInferenceRequest(req *http.Request) bool {
	if req.Method != http.MethodPost {
		return false
	}
	path := strings.TrimRight(req.URL.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/transcribe", "/audio/transcriptions", "/voices/enroll"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func (t *inferenceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !isInferenceRequest(req) {
		return t.base.RoundTrip(req)
	}
	closeRequest := func() {
		if req.Body != nil {
			_ = req.Body.Close()
		}
	}
	if err := req.Context().Err(); err != nil {
		closeRequest()
		return nil, err
	}
	select {
	case t.gate.slots <- struct{}{}:
	case <-req.Context().Done():
		closeRequest()
		return nil, req.Context().Err()
	}
	var once sync.Once
	release := func() { once.Do(func() { <-t.gate.slots }) }
	// A canceled waiter may win the ready-slot select. Do not send its request.
	if err := req.Context().Err(); err != nil {
		release()
		closeRequest()
		return nil, err
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		release()
		return resp, err
	}
	if resp == nil {
		release()
		return nil, fmt.Errorf("inference transport returned no response")
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		release()
		return resp, nil
	}
	// Returning headers does not mean computation or streaming has finished.
	// Hold this one request until its body is consumed, fails, or is closed.
	resp.Body = &inferenceBody{ReadCloser: resp.Body, release: release}
	return resp, nil
}

type inferenceBody struct {
	io.ReadCloser
	release   func()
	closeOnce sync.Once
	closeErr  error
}

func (b *inferenceBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.release()
	}
	return n, err
}

func (b *inferenceBody) Close() error {
	b.closeOnce.Do(func() {
		defer b.release()
		b.closeErr = b.ReadCloser.Close()
	})
	return b.closeErr
}
