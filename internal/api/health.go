package api

import (
	"net/http"
	"sync/atomic"
)

// ready — принимаем ли мы ещё трафик. Пакетная переменная, а не поле: ручку
// /readyz регистрирует роутер, а дёргает её при остановке main, и тащить общий
// объект через оба места ради одного флага смысла нет.
var ready atomic.Bool

// Изначально «готов»: ручка отвечает 200 с первого запроса, не дожидаясь,
// чтобы кто-нибудь её включил.
func init() { ready.Store(true) }

// SetReady переключает готовность. Вызывается один раз — при начале остановки,
// ДО того как закрывается HTTP-сервер: балансировщику нужно успеть увести
// трафик, пока мы ещё дорабатываем принятые запросы.
func SetReady(v bool) { ready.Store(v) }

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

// ReadyzHandler — readiness probe. queueBroken отвечает на вопрос «разбор
// очереди мёртв?»; nil означает, что спрашивать не у кого (так поднимается
// debug-сервер: он вообще без Redis и воркеров).
//
// Проверка очереди появилась после нагрузочной 2026-08-13. До неё ручка знала
// ровно одно — выключают нас или нет, — и отвечала «готов» сервису, который
// принимал задачи и не разбирал ни одной. Мониторингу смотреть было не на
// что: процесс жив и вовсю занят.
//
// @Summary      Readiness probe
// @Description  Возвращает 200 когда сервис готов принимать трафик
// @Tags         health
// @Produce      plain
// @Success      200 {string} string "ok"
// @Failure      503 {string} string "shutting down | queue is not being processed"
// @Router       /readyz [get]
func ReadyzHandler(queueBroken func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		// Выключение проверяется ПЕРВЫМ — ради текста, код в обоих случаях 503.
		//
		// При остановке воркеров гасят раньше HTTP, очередь перестаёт
		// опрашиваться, и через queueStaleAfter она честно считается мёртвой.
		// Спросив сначала её, ручка отвечала бы дежурному «queue is not being
		// processed» на штатном выключении — то есть звала бы чинить то, что
		// не сломано.
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("shutting down"))
			return
		}
		if queueBroken != nil && queueBroken() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("queue is not being processed"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}
