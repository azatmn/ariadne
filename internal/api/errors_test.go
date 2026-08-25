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

// Соответствие «код ошибки → код HTTP». Это публичный контракт из ТЗ: backend
// разбирает ответ по обоим, и подмена 413 на 400 увела бы его в починку
// собственных данных вместо разбиения трека на части.
//
// Проверяется вся таблица разом, а не выборочно: коды добавляют по одному, и
// новый легко забыть внести в codeToStatus — тогда он молча станет пятисоткой.
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

// Форма тела ошибки: обёртка error с полями code и message, заголовок
// application/json. Форма согласована по ТЗ и разбирается на стороне PHP —
// переименовать поле нельзя даже ради красоты.
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

// requestId в тело НЕ кладём — он остаётся только в заголовке X-Request-ID.
func TestWriteErrorRequestIDNotInBody(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	w.Header().Set("X-Request-ID", "test-req-123")

	WriteError(w, r, CodeInvalidRequest, "test")

	var payload ErrorPayload
	err := json.NewDecoder(w.Body).Decode(&payload)
	require.NoError(t, err, "failed to decode")
	assert.Nil(t, payload.Error.Details, "requestId не должен попадать в тело")
	assert.Equal(t, "test-req-123", w.Header().Get("X-Request-ID"), "id остаётся в заголовке")
}

// Внутри AppError лежат два текста, и Error() отдаёт ВНУТРЕННИЙ. Он идёт в
// лог, где нужна настоящая причина («connection refused»), а не вежливая
// формулировка для клиента.
func TestAppErrorWithWrappedError(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	appErr := &AppError{Code: CodeInternal, Message: "service unavailable", Err: inner}

	assert.Equal(t, "connection refused", appErr.Error())
}

// Обратный случай: оборачивать нечего — ошибку породили мы сами, проверив
// запрос. Тогда Error() отдаёт Message, и в логе не оказывается пустой строки.
func TestAppErrorWithoutWrappedError(t *testing.T) {
	appErr := &AppError{Code: CodeInvalidRequest, Message: "missing field"}

	assert.Equal(t, "missing field", appErr.Error())
}

// Код, которого нет в таблице. Такое случается при опечатке в имени
// константы: ответ уйдёт пятисоткой, но сервис не упадёт и запрос не оборвётся.
//
// Соврать про причину плохо, но уронить запрос из-за опечатки — хуже.
func TestWriteErrorUnknownCode(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	WriteError(w, r, "SOMETHING_WEIRD", "oops")

	assert.Equal(t, 500, w.Code)
}
