package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteErrorStatus(t *testing.T) {
	tests := []struct {
		code       string
		wantStatus int
	}{
		{CodeInvalidRequest, 400},
		{CodeInvalidRouteFormat, 400},
		{CodeRouteTooLarge, 413},
		{CodeUnprocessableRoute, 422},
		{CodeInternal, 500},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/", nil)

			WriteError(w, r, tt.code, "test message")

			if w.Code != tt.wantStatus {
				t.Errorf("code %s: want status %d, got %d", tt.code, tt.wantStatus, w.Code)
			}
		})
	}
}

func TestWriteErrorJSON(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	WriteError(w, r, CodeInvalidRequest, "field X is required")

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %s", ct)
	}

	var payload ErrorPayload
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if payload.Error.Code != CodeInvalidRequest {
		t.Errorf("code: want %s, got %s", CodeInvalidRequest, payload.Error.Code)
	}
	if payload.Error.Message != "field X is required" {
		t.Errorf("message: want %q, got %q", "field X is required", payload.Error.Message)
	}
}

func TestWriteErrorUnknownCode(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	WriteError(w, r, "SOMETHING_WEIRD", "oops")

	if w.Code != 500 {
		t.Errorf("unknown code: want status 500, got %d", w.Code)
	}
}
