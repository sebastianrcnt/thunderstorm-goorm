package v1

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHttpPostRelaysRequestAndResponse(t *testing.T) {
	errCh := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(format string, args ...interface{}) {
			errCh <- strings.TrimSpace(strings.ReplaceAll(fmt.Sprintf(format, args...), "\n", " "))
			http.Error(w, "bad request", http.StatusBadRequest)
		}
		if r.Method != http.MethodPost {
			fail("method = %s, want POST", r.Method)
			return
		}
		if got := r.URL.Query().Get("q"); got != "search" {
			fail("query q = %q, want search", got)
			return
		}
		if got := r.Header.Get("X-Test"); got != "header-value" {
			fail("X-Test = %q, want header-value", got)
			return
		}
		cookie, err := r.Cookie("session")
		if err != nil {
			fail("missing session cookie: %v", err)
			return
		}
		if cookie.Value != "cookie-value" {
			fail("session cookie = %q, want cookie-value", cookie.Value)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			fail("read body: %v", err)
			return
		}
		if string(body) != "payload" {
			fail("body = %q, want payload", string(body))
			return
		}

		w.Header().Set("X-Reply", "reply-value")
		http.SetCookie(w, &http.Cookie{Name: "reply", Value: "cookie"})
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	server := NewGoormRpcServer("auto", false)
	resp, err := server.HttpPost(context.Background(), &HttpRequest{
		Url:       upstream.URL,
		Headers:   map[string]string{"X-Test": "header-value"},
		Query:     map[string]string{"q": "search"},
		Cookies:   map[string]string{"session": "cookie-value"},
		Body:      []byte("payload"),
		TimeoutMs: 1000,
	})
	if err != nil {
		t.Fatalf("HttpPost returned error: %v", err)
	}
	select {
	case msg := <-errCh:
		t.Fatal(msg)
	default:
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	if got := resp.Headers["X-Reply"]; got != "reply-value" {
		t.Fatalf("X-Reply = %q, want reply-value", got)
	}
	if got := resp.Cookies["reply"]; got != "cookie" {
		t.Fatalf("reply cookie = %q, want cookie", got)
	}
	if string(resp.Body) != "ok" {
		t.Fatalf("response body = %q, want ok", string(resp.Body))
	}
}

func TestHttpGetHonorsTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer upstream.Close()

	server := NewGoormRpcServer("auto", false)
	_, err := server.HttpGet(context.Background(), &HttpRequest{
		Url:       upstream.URL,
		TimeoutMs: 1,
	})
	if err == nil {
		t.Fatal("HttpGet returned nil error, want timeout error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
}

func TestHttpGetHonorsCanceledContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	server := NewGoormRpcServer("auto", false)
	_, err := server.HttpGet(ctx, &HttpRequest{
		Url:       upstream.URL,
		TimeoutMs: 1000,
	})
	if err == nil {
		t.Fatal("HttpGet returned nil error, want context canceled")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestHttpGetRejectsNegativeTimeout(t *testing.T) {
	server := NewGoormRpcServer("auto", false)
	_, err := server.HttpGet(context.Background(), &HttpRequest{
		Url:       "http://example.com",
		TimeoutMs: -1,
	})
	if err == nil {
		t.Fatal("HttpGet returned nil error, want validation error")
	}
	if !strings.Contains(err.Error(), "timeout_ms") {
		t.Fatalf("error = %v, want timeout_ms validation error", err)
	}
}

func TestHttpGetRejectsUnsupportedURLScheme(t *testing.T) {
	server := NewGoormRpcServer("auto", false)
	_, err := server.HttpGet(context.Background(), &HttpRequest{
		Url: "file:///etc/passwd",
	})
	if err == nil {
		t.Fatal("HttpGet returned nil error, want unsupported scheme error")
	}
	if !strings.Contains(err.Error(), "unsupported url scheme") {
		t.Fatalf("error = %v, want unsupported scheme error", err)
	}
}

func TestHttpGetAppliesPathParams(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/users/kim%20space" {
			http.Error(w, got, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	server := NewGoormRpcServer("auto", false)
	resp, err := server.HttpGet(context.Background(), &HttpRequest{
		Url:    upstream.URL + "/users/{id}",
		Params: map[string]string{"id": "kim space"},
	})
	if err != nil {
		t.Fatalf("HttpGet returned error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", resp.StatusCode, http.StatusOK, string(resp.Body))
	}
}

func TestHttpGetRejectsBody(t *testing.T) {
	server := NewGoormRpcServer("auto", false)
	_, err := server.HttpGet(context.Background(), &HttpRequest{
		Url:  "http://example.com",
		Body: []byte("unexpected"),
	})
	if err == nil {
		t.Fatal("HttpGet returned nil error, want body validation error")
	}
	if !strings.Contains(err.Error(), "GET request body") {
		t.Fatalf("error = %v, want GET body validation error", err)
	}
}

func TestHttpGetRejectsOversizedResponseBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), maxResponseBodyBytes+1))
	}))
	defer upstream.Close()

	server := NewGoormRpcServer("auto", false)
	_, err := server.HttpGet(context.Background(), &HttpRequest{
		Url: upstream.URL,
	})
	if err == nil {
		t.Fatal("HttpGet returned nil error, want oversized body error")
	}
	if !strings.Contains(err.Error(), "response body exceeds") {
		t.Fatalf("error = %v, want oversized body error", err)
	}
}

func TestHttpPostRejectsOversizedRequestBody(t *testing.T) {
	server := NewGoormRpcServer("auto", false)
	_, err := server.HttpPost(context.Background(), &HttpRequest{
		Url:  "http://example.com",
		Body: bytes.Repeat([]byte("a"), maxRequestBodyBytes+1),
	})
	if err == nil {
		t.Fatal("HttpPost returned nil error, want oversized body error")
	}
	if !strings.Contains(err.Error(), "request body exceeds") {
		t.Fatalf("error = %v, want oversized body error", err)
	}
}

func TestMetricsHandlerExportsPrometheusText(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	MetricsHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "goorm_rpc_http_requests_total") {
		t.Fatalf("metrics body = %q, want requests metric", body)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", rec.Header().Get("Content-Type"))
	}
}

func TestDirectBindRequiresDevice(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewGoormRpcServer did not panic")
		}
	}()

	NewGoormRpcServer("auto", true)
}
