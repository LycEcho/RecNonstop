package infer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gateTestTransport func(*http.Request) (*http.Response, error)

func (f gateTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func gateTestRequest(t *testing.T, ctx context.Context, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func assertGateBlocked(t *testing.T, transport http.RoundTripper, url string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	resp, err := transport.RoundTrip(gateTestRequest(t, ctx, http.MethodPost, url))
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request entered an occupied inference slot: err=%v", err)
	}
}

func TestInferenceGateSharesCapacityAcrossClientsAndBypassesStatus(t *testing.T) {
	gate := NewInferenceGate(0)
	var inferenceCalls atomic.Int32
	base := gateTestTransport(func(r *http.Request) (*http.Response, error) {
		body := io.ReadCloser(http.NoBody)
		if isInferenceRequest(r) {
			inferenceCalls.Add(1)
			body = io.NopCloser(strings.NewReader(`{"result":"synthetic"}`))
		}
		return &http.Response{StatusCode: 200, Body: body}, nil
	})
	textClient, asrClient := gate.Transport(base), gate.Transport(base)
	textResponse, err := textClient.RoundTrip(gateTestRequest(t, context.Background(), http.MethodPost, "http://text.invalid/v1/chat/completions"))
	if err != nil {
		t.Fatal(err)
	}
	defer textResponse.Body.Close()
	// Headers have already returned, but the body is still live. A different
	// client's ASR request must wait rather than starting computation.
	assertGateBlocked(t, asrClient, "http://asr.invalid/v1/transcribe")
	for _, endpoint := range []struct{ method, path string }{{http.MethodGet, "/health"}, {http.MethodGet, "/v1/models"}, {http.MethodPost, "/api/status"}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		resp, err := asrClient.RoundTrip(gateTestRequest(t, ctx, endpoint.method, "http://asr.invalid"+endpoint.path))
		cancel()
		if err != nil {
			t.Fatalf("status endpoint blocked: %v", err)
		}
		_ = resp.Body.Close()
	}
	if inferenceCalls.Load() != 1 {
		t.Fatalf("ASR ran before text body closed: %d", inferenceCalls.Load())
	}
	if err := textResponse.Body.Close(); err != nil {
		t.Fatal(err)
	}
	resp, err := asrClient.RoundTrip(gateTestRequest(t, context.Background(), http.MethodPost, "http://asr.invalid/v1/transcribe"))
	if err != nil || inferenceCalls.Load() != 2 {
		t.Fatalf("ASR did not acquire released slot: calls=%d err=%v", inferenceCalls.Load(), err)
	}
	_ = resp.Body.Close()
}

func TestInferenceGateEOFReleasesOnceWithoutRequiringClose(t *testing.T) {
	transport := NewInferenceGate(1).Transport(gateTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("complete"))}, nil
	}))
	request := func() *http.Response {
		t.Helper()
		resp, err := transport.RoundTrip(gateTestRequest(t, context.Background(), http.MethodPost, "http://text.invalid/v1/chat/completions"))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	first := request()
	if _, err := io.ReadAll(first.Body); err != nil {
		t.Fatal(err)
	}
	second := request() // Consuming EOF has released the first request's slot.
	defer second.Body.Close()
	_ = first.Body.Close()
	_ = first.Body.Close()
	// Closing an already-consumed response must not release the second request.
	assertGateBlocked(t, transport, "http://asr.invalid/v1/voices/enroll")
}

type gateErrorBody struct {
	err    error
	closed atomic.Int32
}

func (b *gateErrorBody) Read([]byte) (int, error) { return 0, b.err }
func (b *gateErrorBody) Close() error {
	b.closed.Add(1)
	return b.err
}

func TestInferenceGateCancellationClosesUnsentRequest(t *testing.T) {
	gate := NewInferenceGate(1)
	transport := gate.Transport(gateTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("held"))}, nil
	}))
	resp, err := transport.RoundTrip(gateTestRequest(t, context.Background(), http.MethodPost, "http://text.invalid/v1/chat/completions"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, alreadyCanceled := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		if alreadyCanceled {
			cancel()
		}
		req := gateTestRequest(t, ctx, http.MethodPost, "http://asr.invalid/v1/transcribe")
		body := &gateErrorBody{err: io.EOF}
		req.Body = body
		_, err := transport.RoundTrip(req)
		cancel()
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("canceled queue returned %v", err)
		}
		if body.closed.Load() != 1 {
			t.Fatal("transport did not close the request body it declined to send")
		}
	}
}

func TestInferenceGateFailurePathsReleaseCapacity(t *testing.T) {
	failure := errors.New("synthetic transport/body failure")
	for _, mode := range []string{"transport error", "response and error", "nil response", "empty body", "read error", "close error", "HTTP error"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			failedBody := &gateErrorBody{err: failure}
			transport := NewInferenceGate(1).Transport(gateTestTransport(func(*http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
				}
				switch mode {
				case "transport error":
					return nil, failure
				case "response and error":
					return &http.Response{Body: failedBody}, failure
				case "nil response":
					return nil, nil
				case "empty body":
					return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
				case "read error", "close error":
					return &http.Response{StatusCode: 200, Body: failedBody}, nil
				default:
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("temporary error"))}, nil
				}
			}))
			req := gateTestRequest(t, context.Background(), http.MethodPost, "http://text.invalid/v1/chat/completions")
			resp, err := transport.RoundTrip(req)
			if mode == "transport error" || mode == "response and error" {
				if !errors.Is(err, failure) {
					t.Fatal("transport error was swallowed")
				}
			}
			if mode == "read error" {
				if _, err := io.ReadAll(resp.Body); !errors.Is(err, failure) {
					t.Fatal("body read error was swallowed")
				}
			}
			if mode == "close error" {
				if err := resp.Body.Close(); !errors.Is(err, failure) {
					t.Fatal("body close error was swallowed")
				}
			}
			if mode == "HTTP error" {
				assertGateBlocked(t, transport, "http://asr.invalid/v1/transcribe")
				_ = resp.Body.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			next, err := transport.RoundTrip(gateTestRequest(t, ctx, http.MethodPost, "http://asr.invalid/v1/audio/transcriptions"))
			if err != nil || calls != 2 {
				t.Fatalf("failure leaked slot: calls=%d err=%v", calls, err)
			}
			_ = next.Body.Close()
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if mode == "response and error" && failedBody.closed.Load() == 0 {
				t.Fatal("failed response body leaked")
			}
		})
	}
}

func TestInferenceGateConfiguredParallelismAndConcurrentRequests(t *testing.T) {
	const limit = 2
	gate := NewInferenceGate(limit)
	var active, maximum atomic.Int32
	base := gateTestTransport(func(*http.Request) (*http.Response, error) {
		current := active.Add(1)
		for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
		}
		return &http.Response{StatusCode: 200, Body: &gateCountedBody{Reader: strings.NewReader("synthetic response"), done: func() { active.Add(-1) }}}, nil
	})
	text, asr := gate.Transport(base), gate.Transport(base)
	first, _ := text.RoundTrip(gateTestRequest(t, context.Background(), http.MethodPost, "http://text.invalid/v1/chat/completions"))
	second, _ := asr.RoundTrip(gateTestRequest(t, context.Background(), http.MethodPost, "http://asr.invalid/v1/transcribe"))
	assertGateBlocked(t, text, "http://text.invalid/v1/chat/completions")
	_ = first.Body.Close()
	_ = second.Body.Close()
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			transport, url := text, "http://text.invalid/v1/chat/completions"
			if i%2 == 0 {
				transport, url = asr, "http://asr.invalid/v1/transcribe"
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			resp, err := transport.RoundTrip(gateTestRequest(t, ctx, http.MethodPost, url))
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}(i)
	}
	wg.Wait()
	if maximum.Load() != limit || active.Load() != 0 {
		t.Fatalf("shared limit not respected: max=%d active=%d", maximum.Load(), active.Load())
	}
}

type gateCountedBody struct {
	io.Reader
	done func()
	once sync.Once
}

func (b *gateCountedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err != nil {
		b.once.Do(b.done)
	}
	return n, err
}

func (b *gateCountedBody) Close() error {
	b.once.Do(b.done)
	return nil
}
