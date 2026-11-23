package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) *server { return testServerLimit(t, 100) }

func testServerLimit(t *testing.T, limit int) *server {
	t.Helper()
	return &server{
		cfg:     config{allowedOrigins: map[string]bool{"https://site.example": true}},
		limiter: newLimiter(limit, time.Hour),
		store:   &store{path: filepath.Join(t.TempDir(), "m.jsonl")},
	}
}

func post(s *server, body, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/contact", strings.NewReader(body))
	req.RemoteAddr = "1.2.3.4:5555"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	return rr
}

const good = `{"name":"Sam","email":"sam@example.com","message":"Hello, I have a project for you."}`

func TestValidMessageSaved(t *testing.T) {
	s := testServer(t)
	if rr := post(s, good, "https://site.example"); rr.Code != 200 {
		t.Fatalf("got %d: %s", rr.Code, rr.Body)
	}
}

func TestValidation(t *testing.T) {
	s := testServer(t)
	for _, b := range []string{
		`{"name":"S","email":"sam@example.com","message":"Hello, I have a project."}`,
		`{"name":"Sam","email":"nope","message":"Hello, I have a project."}`,
		`{"name":"Sam","email":"sam@example.com","message":"short"}`,
		`not json`,
	} {
		if rr := post(s, b, ""); rr.Code != 400 {
			t.Errorf("body %q: got %d want 400", b, rr.Code)
		}
	}
}

func TestHoneypot(t *testing.T) {
	s := testServer(t)
	rr := post(s, `{"name":"Bot","email":"b@example.com","message":"buy cheap stuff now","website":"x.com"}`, "")
	if rr.Code != 200 {
		t.Fatalf("honeypot should look like success, got %d", rr.Code)
	}
}

func TestRateLimit(t *testing.T) {
	s := testServerLimit(t, 2)
	post(s, good, "")
	post(s, good, "")
	if rr := post(s, good, ""); rr.Code != 429 {
		t.Fatalf("got %d want 429", rr.Code)
	}
}

func TestCORSBlocksUnknownOrigin(t *testing.T) {
	s := testServer(t)
	if rr := post(s, good, "https://evil.example"); rr.Code != 403 {
		t.Fatalf("got %d want 403", rr.Code)
	}
}
