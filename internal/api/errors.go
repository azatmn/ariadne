package api

import (
	"encoding/json"
	"net/http"

	"ariadne/internal/logger"
)

// Машиночитаемые коды ошибок (по ТЗ). Публичный API: backend разбирает их
// строками, менять и переименовывать нельзя.
const (
	CodeInvalidRequest     = "INVALID_REQUEST"
	CodeInvalidRouteFormat = "INVALID_ROUTE_FORMAT"
	CodeRouteTooLarge      = "ROUTE_TOO_LARGE"
	CodeUnprocessableRoute = "UNPROCESSABLE_ROUTE"
	CodeNotFound           = "NOT_FOUND"
	CodeInternal           = "INTERNAL"
)

// ErrorPayload — формат ошибки в JSON-ответе. requestId — в заголовке
// X-Request-ID, не в теле. details зарезервировано под будущий машиночитаемый
// контекст (напр. поле валидации), сейчас не заполняется (omitempty → отсутствует).
//
//	{
//	  "error": {
//	    "code": "INVALID_REQUEST",
//	    "message": "..."
//	  }
//	}
type ErrorPayload struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody — содержимое поля error. Code машиночитаемый и стабильный:
// по нему backend принимает решение, а Message только для человека и может
// меняться без предупреждения.
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
	CodeNotFound:           http.StatusNotFound,
	CodeInternal:           http.StatusInternalServerError,
}

// AppError — ошибка хендлера, знающая свой код ответа.
//
// Message уходит клиенту, Err остаётся в логе. Разделение намеренное:
// внутренний текст может содержать адрес Redis, кусок чужого трека или
// строку запроса — наружу такому нельзя.
type AppError struct {
	Code    string
	Message string
	Err     error
}

// Error отдаёт текст обёрнутой ошибки, а при её отсутствии — Message.
// Так *AppError годится и для errors.Is/As по исходной причине.
func (e *AppError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Message
}

// WriteError пишет ошибку в ответ: код HTTP по таблице codeToStatus, тело —
// ErrorPayload.
//
// Незнакомый код превращается в 500, а не в панику: соврать про причину хуже,
// чем промолчать, но уронить запрос — хуже обоих.
func WriteError(w http.ResponseWriter, r *http.Request, code, message string) {
	status, ok := codeToStatus[code]
	if !ok {
		status = http.StatusInternalServerError
	}

	// requestId в тело НЕ кладём — он и так в заголовке X-Request-ID
	// (единообразно с gRPC, где id только в метаданных). details оставлено
	// на будущее (например, поле валидации), сейчас не заполняется.
	payload := ErrorPayload{
		Error: ErrorBody{
			Code:    code,
			Message: message,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logger.FromContext(r.Context()).Error("failed to write error response", "error", err)
	}
}
