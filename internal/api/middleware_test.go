package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Паника в хендлере превращается в 500 с обычным телом ошибки.
//
// Без этого паника уносит ВЕСЬ процесс вместе со всеми параллельными
// запросами. Проверяется не только код, но и разбираемое тело: клиент должен
// получить тот же формат, что и при любой другой ошибке, а не пустой ответ.
func TestRecoverCatchesPanic(t *testing.T) {
	logger := slog.Default()

	handler := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("something went wrong")
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var payload ErrorPayload
	err := json.NewDecoder(w.Body).Decode(&payload)
	require.NoError(t, err, "failed to decode response")
	assert.Equal(t, CodeInternal, payload.Error.Code)
}

// Зеркало: обычный запрос проходит насквозь и не превращается в 500.
// Без этой половины «отвечать 500 всегда» прошло бы предыдущий тест.
func TestRecoverNoPanic(t *testing.T) {
	logger := slog.Default()

	handler := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
}

// Тело в пределах лимита доходит до хендлера целиком и БЕЗ ошибки.
//
// Ошибка проверяется отдельно от содержимого: обёртка, обрывающая чтение на
// каждом запросе, отдала бы то же самое усечённое тело — только пустое, а
// пустое тело сравнением с «small body» не поймать.
func TestLimitBodyAllows(t *testing.T) {
	var readErr error
	handler := LimitBody(100)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		body, readErr = io.ReadAll(r.Body)
		_, _ = w.Write(body)
	}))

	// 10 байт — проходит
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("small body"))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	require.NoError(t, readErr, "тело в пределах лимита обязано читаться без ошибки")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "small body", w.Body.String())
}

// Тело больше лимита ОБРЫВАЕТСЯ ОШИБКОЙ, а не обрезается молча.
//
// Разница решающая, и раньше тест её не видел: он смотрел только на то, что
// до хендлера дошло меньше двадцати байт. Молчаливое усечение дало бы ровно
// тот же результат — и вот чем это кончилось бы.
//
// Усечённое тело выглядит годным. Оно уехало бы в codec, тот споткнулся бы на
// оборванном zlib и ответил «битый вход». Клиент пошёл бы чинить свои данные,
// с которыми всё в порядке, вместо того чтобы слать трек по частям. Ошибка
// же опознаётся по типу и превращается в честное «request body too large».
//
// Проверяется и сам лимит из ошибки: обёртка, поставившая свой потолок вместо
// заданного, прошла бы проверку по одному типу.
func TestLimitBodyRejects(t *testing.T) {
	var readErr error
	var got []byte
	handler := LimitBody(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, readErr = io.ReadAll(r.Body)
	}))

	// 20 байт при лимите в 10
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("this body is too big"))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	var maxErr *http.MaxBytesError
	require.ErrorAs(t, readErr, &maxErr,
		"чтение тела сверх лимита обязано давать *http.MaxBytesError, а не молча усекать")
	assert.Equal(t, int64(10), maxErr.Limit, "в ошибке обязан стоять заданный лимит")
	assert.LessOrEqual(t, len(got), 10, "прочитать больше лимита нельзя")
}

// Обёртка отдаёт исходный ResponseWriter через Unwrap.
//
// Выглядит формальностью, но без неё обёртка съедает http.Flusher и
// http.Hijacker: стандартная библиотека добирается до них именно через Unwrap.
// Проявилось бы это не здесь, а в потоковой отдаче или апгрейде соединения.
func TestStatusWriterUnwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: inner}

	assert.Equal(t, inner, sw.Unwrap(), "Unwrap обязан отдавать исходный ResponseWriter")
}

// Логирующая обёртка не вмешивается в ответ: код и тело доходят до клиента
// нетронутыми. Она подменяет ResponseWriter своим, чтобы запомнить код, —
// и это ровно то место, где легко потерять записанное хендлером.
func TestLoggerPassesThrough(t *testing.T) {
	logger := slog.Default()

	handler := Logger(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))

	r := httptest.NewRequest(http.MethodPost, "/v1/routes/resolve-collisions", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "ok", w.Body.String())
}

// Идентификатор запроса появляется в заголовке X-Request-ID. По нему клиент
// ссылается на конкретный запрос, когда приходит разбираться.
func TestRequestIDAddsHeader(t *testing.T) {
	logger := slog.Default()

	handler := RequestID(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	assert.NotEmpty(t, w.Header().Get("X-Request-ID"), "заголовок X-Request-ID обязан быть выставлен")
}

// И он РАЗНЫЙ у разных запросов. Половина смысла идентификатора в этом:
// одинаковый на всех склеил бы в логе четыре параллельные задачи в одну кашу,
// и разобрать их было бы уже нечем.
func TestRequestIDUnique(t *testing.T) {
	logger := slog.Default()

	handler := RequestID(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r1 := httptest.NewRequest(http.MethodGet, "/", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, r1)

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, r2)

	id1 := w1.Header().Get("X-Request-ID")
	id2 := w2.Header().Get("X-Request-ID")

	assert.NotEqual(t, id1, id2, "expected unique IDs")
}
