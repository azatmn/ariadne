package api

import "net/http"

// Healthz — liveness probe.
// @Summary      Liveness probe
// @Description  Возвращает 200 пока процесс жив
// @Tags         health
// @Produce      plain
// @Success      200 {string} string "ok"
// @Router       /healthz [get]
func Healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// Readyz — readiness probe.
// @Summary      Readiness probe
// @Description  Возвращает 200 когда сервис готов принимать трафик
// @Tags         health
// @Produce      plain
// @Success      200 {string} string "ok"
// @Router       /readyz [get]
func Readyz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
