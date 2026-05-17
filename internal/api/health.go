package api

import "net/http"

// Healthz — liveness probe: 200 ok всегда, пока процесс жив.
func Healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// Readyz — readiness probe.
// MVP: то же что healthz. Пост-MVP: при USE_OSRM=true — пинговать OSRM.
func Readyz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
