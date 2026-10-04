package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeadlineState(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		deadline time.Time
		want     string
	}{
		{"elapsed", now.Add(-time.Minute), "past_due"},
		{"tomorrow", now.Add(24 * time.Hour), "due_soon"},
		{"boundary", now.Add(48 * time.Hour), "due_soon"},
		{"next week", now.Add(7 * 24 * time.Hour), "on_track"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deadlineState(now, tc.deadline); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEnvelopeRejection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("request auth or method missing")
		}
		if r.URL.Path != "/pdf/ocr" {
			t.Errorf("request path = %q, want /pdf/ocr", r.URL.Path)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"INVALID_ARGUMENT","message":"pdf required"}}`))
	}))
	defer upstream.Close()
	g := newGateway("test-key")
	g.base = upstream.URL
	g.http = upstream.Client()
	var data any
	err := g.post(context.Background(), "/pdf/ocr", map[string]string{"pdf": "course.pdf"}, &data, "course:ocr")
	var refusal rejected
	if !errors.As(err, &refusal) || refusal.Code != "INVALID_ARGUMENT" {
		t.Fatalf("unexpected upstream result: %v", err)
	}
	w := httptest.NewRecorder()
	respondError(w, err)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "pdf required") {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}
