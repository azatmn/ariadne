package main

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Порядок остановки: сперва перестаём принимать, потом дорабатываем взятое.
//
// Обратный порядок — не мелочь стиля. Воркеры уже погашены, а HTTP и gRPC ещё
// мгновение принимают: задача из этого окна получает «принято» и номерок, а
// разбирать её некому. Клиент опрашивает её впустую до следующего запуска.
//
// Тест тонкий намеренно: проверять тут нечего, кроме порядка, — но именно
// порядок и был перепутан, и в длинном main перестановку двух блоков не
// заметит никто.
func TestGracefulStop_StopsAcceptingBeforeDraining(t *testing.T) {
	var (
		mu    sync.Mutex
		order []string
	)
	record := func(what string) func() {
		return func() {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, what)
		}
	}

	gracefulStop(record("accept"), record("drain"))

	require.Len(t, order, 2, "обе части обязаны выполниться")
	assert.Equal(t, []string{"accept", "drain"}, order,
		"приём закрывается ПЕРВЫМ, иначе принятую задачу некому будет разобрать")
}

// Дренаж не должен начинаться, пока приём не остановлен ПОЛНОСТЬЮ.
//
// Отдельная проверка от предыдущей: запись в список могла бы случиться и при
// параллельном запуске обеих частей — порядок в срезе тогда зависел бы от
// планировщика. Здесь дренаж прямо спрашивает, закончился ли приём.
func TestGracefulStop_DrainWaitsForAcceptToFinish(t *testing.T) {
	acceptDone := false
	drainSawAcceptDone := false

	gracefulStop(
		func() { acceptDone = true },
		func() { drainSawAcceptDone = acceptDone },
	)

	assert.True(t, drainSawAcceptDone,
		"дренаж начался раньше, чем приём успел закрыться")
}
