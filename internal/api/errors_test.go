package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

			assert.Equal(t, tt.wantStatus, w.Code, "code %s", tt.code)
		})
	}
}

func TestWriteErrorJSON(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	WriteError(w, r, CodeInvalidRequest, "field X is required")

	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var payload ErrorPayload
	err := json.NewDecoder(w.Body).Decode(&payload)
	require.NoError(t, err, "failed to decode response")

	assert.Equal(t, CodeInvalidRequest, payload.Error.Code)
	assert.Equal(t, "field X is required", payload.Error.Message)
}

func TestWriteErrorWithRequestID(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	w.Header().Set("X-Request-ID", "test-req-123")

	WriteError(w, r, CodeInvalidRequest, "test")

	var payload ErrorPayload
	err := json.NewDecoder(w.Body).Decode(&payload)
	require.NoError(t, err, "failed to decode")
	require.NotNil(t, payload.Error.Details, "expected details with requestId")
	assert.Equal(t, "test-req-123", payload.Error.Details["requestId"])
}

func TestAppErrorWithWrappedError(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	appErr := &AppError{Code: CodeInternal, Message: "service unavailable", Err: inner}

	assert.Equal(t, "connection refused", appErr.Error())
}

func TestAppErrorWithoutWrappedError(t *testing.T) {
	appErr := &AppError{Code: CodeInvalidRequest, Message: "missing field"}

	assert.Equal(t, "missing field", appErr.Error())
}

func TestWriteErrorUnknownCode(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	WriteError(w, r, "SOMETHING_WEIRD", "oops")

	assert.Equal(t, 500, w.Code)
}
