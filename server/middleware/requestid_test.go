package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestIDGeneratesAndPropagates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, GetRequestID(c)) })

	// Generated when the client sends none, and echoed in the response header.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.ServeHTTP(w, req)
	generated := w.Body.String()
	if len(generated) != 32 {
		t.Fatalf("expected 32-char request id, got %q", generated)
	}
	if got := w.Header().Get("X-Request-ID"); got != generated {
		t.Fatalf("response header %q != %q", got, generated)
	}

	// Preserved when the client (or an upstream proxy) supplies one.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Request-ID", "trace-123")
	r.ServeHTTP(w, req)
	if w.Body.String() != "trace-123" {
		t.Fatalf("expected propagated id, got %q", w.Body.String())
	}
}

func TestRequestIDRejectsOverlongHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, GetRequestID(c)) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Request-ID", strings.Repeat("a", 200))
	r.ServeHTTP(w, req)
	if len(w.Body.String()) != 32 {
		t.Fatalf("overlong id should be replaced, got %q", w.Body.String())
	}
}
