package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	_ "ariadne/swagger"

	"github.com/stretchr/testify/assert"
)

// Хендлеру для health/swagger маршрутов store не нужен (они его не трогают).

// Обе пробы отвечают через СОБРАННЫЙ роутер, а не вызовом функции напрямую.
//
// Разница в том, что тут работает вся цепочка middleware. Пробы дёргает
// оркестратор каждые несколько секунд, и любая обёртка, решившая ответить за
// них своё, вывела бы сервис из балансировки без единой ошибки в логе.
//
// Проверяется и тело «ok», не только код: некоторые проверки читают именно его.
//
// queueBroken здесь nil — это режим без очереди (так поднимается debug-сервер),
// и /readyz обязан в нём отвечать 200, а не считать молчащую очередь поломкой.
func TestIntegrationHealthEndpoints(t *testing.T) {
	cfg := testConfig()
	cfg.MaxBodyBytes = 1 << 20
	logger := testLogger()
	router := NewRouter(NewHandler(nil), logger, cfg.MaxBodyBytes, false, nil)

	tests := []struct {
		name string
		path string
	}{
		{"healthz", "/healthz"},
		{"readyz", "/readyz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()

			router.ServeHTTP(w, r)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, "ok", w.Body.String())
		})
	}
}

// Swagger отдаётся, когда его включили флагом.
//
// Тест держит и импорт `_ "ariadne/swagger"`: сгенерированный пакет
// регистрирует документацию своим init(), и без импорта ручка вернула бы
// пустую страницу. Забыть этот импорт при переносе кода — обычное дело.
func TestIntegrationSwaggerRoute(t *testing.T) {
	cfg := testConfig()
	cfg.MaxBodyBytes = 1 << 20
	logger := testLogger()
	router := NewRouter(NewHandler(nil), logger, cfg.MaxBodyBytes, true, nil)

	r := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
}
