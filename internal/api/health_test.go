package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ручка готовности обязана краснеть, когда разбор очереди мёртв.
//
// Нагрузочная проверка 2026-08-13 показала худший вид отказа: сервис принимал
// задачи, не разбирал ни одной и при этом отвечал «готов». Мониторинг молчал,
// потому что смотреть ему было не на что: процесс жив и очень занят — он жёг
// процессор в пустом цикле.
func TestReadyz_ReportsBrokenQueue(t *testing.T) {
	broken := true
	h := ReadyzHandler(func() bool { return broken })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"очередь не разбирается — принимать трафик сервису нечем")
	assert.Contains(t, rec.Body.String(), "queue")
}

// Зеркальный тест: при исправной очереди ручка обязана отвечать «готов».
//
// Без него предыдущий проходит и на обработчике, который всегда отдаёт 503, —
// а это выкинуло бы сервис из балансировщика насовсем.
func TestReadyz_OkWhenQueueHealthy(t *testing.T) {
	h := ReadyzHandler(func() bool { return false })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

// При выключении ручка обязана отвечать 503.
func TestReadyz_ShutdownWins(t *testing.T) {
	SetReady(false)
	t.Cleanup(func() { SetReady(true) })

	h := ReadyzHandler(func() bool { return false })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "shutting down")
}

// Когда верно и то и другое, причиной называется ВЫКЛЮЧЕНИЕ.
//
// Случай не выдуманный: при остановке воркеров гасят раньше HTTP, очередь
// перестаёт опрашиваться и вскоре честно считается мёртвой. Оба условия
// истинны одновременно, код ответа одинаковый — а вот текст решает, побежит
// ли дежурный ночью чинить очередь, с которой всё в порядке.
//
// Мутация «переставить проверки местами» первую версию тестов пережила:
// ни один не подавал оба условия сразу.
func TestReadyz_ShutdownExplainedBeforeQueue(t *testing.T) {
	SetReady(false)
	t.Cleanup(func() { SetReady(true) })

	h := ReadyzHandler(func() bool { return true }) // очередь тоже «мертва»

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "shutting down",
		"на штатном выключении нельзя жаловаться на очередь")
	assert.NotContains(t, rec.Body.String(), "queue")
}

// Без проверки очереди ручка работает по-старому.
//
// Нужно для debug-сервера: он поднимается без Redis и воркеров вовсе, и
// требовать от него живую очередь бессмысленно.
func TestReadyz_NilProbeMeansOnlyShutdownMatters(t *testing.T) {
	h := ReadyzHandler(nil)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusOK, rec.Code)
}

// Liveness трогать нельзя: процесс жив, даже когда очередь сломана.
//
// Разделение намеренное. Если /healthz начнёт краснеть от сломанного Redis,
// оркестратор примется перезапускать контейнер по кругу — а перезапуск тут
// ничего не лечит, Redis от этого не поднимется.
func TestHealthz_StaysOkWhenQueueBroken(t *testing.T) {
	rec := httptest.NewRecorder()
	Healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}
