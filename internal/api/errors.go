package api

import (
	"encoding/json"
	"net/http"
)

// Машиночитаемые коды ошибок (по ТЗ).
const (
	CodeInvalidRequest     = "INVALID_REQUEST"
	CodeInvalidRouteFormat = "INVALID_ROUTE_FORMAT"
	CodeRouteTooLarge      = "ROUTE_TOO_LARGE"
	CodeUnprocessableRoute = "UNPROCESSABLE_ROUTE"
	CodeInternal           = "INTERNAL"
)

// ErrorPayload — формат ошибки в JSON-ответе.
//
//	{
//	  "error": {
//	    "code": "INVALID_REQUEST",
//	    "message": "...",
//	    "details": { "requestId": "...", "meta": "..." }
//	  }
//	}
type ErrorPayload struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

var codeToStatus = map[string]int{
	CodeInvalidRequest:     http.StatusBadRequest,
	CodeInvalidRouteFormat: http.StatusBadRequest,
	CodeRouteTooLarge:      http.StatusRequestEntityTooLarge,
	CodeUnprocessableRoute: http.StatusUnprocessableEntity,
	CodeInternal:           http.StatusInternalServerError,
}

type AppError struct {
	Code    string
	Message string
	Err     error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Message
}

func WriteError(w http.ResponseWriter, r *http.Request, code, message string) {
	status, ok := codeToStatus[code]
	if !ok {
		status = http.StatusInternalServerError
	}

	payload := ErrorPayload{
		Error: ErrorBody{
			Code:    code,
			Message: message,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
