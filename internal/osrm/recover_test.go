package osrm

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Паника в дочерней горутине обязана становиться ошибкой, а не убивать процесс.
//
// Проверка нужна отдельно от разбора матрицы: там дыру закрыли, но страховка
// стоит ради СЛЕДУЮЩЕЙ ошибки с индексом. Если её случайно снимут, ни один
// другой тест этого не заметит — процесс упадёт только на кривом ответе от
// живого OSRM, то есть в бою.
func TestPanicErr_ReturnsErrorAndSurvives(t *testing.T) {
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		got []string
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				mu.Lock()
				got = append(got, panicErr(context.Background(), "table", r).Error())
				mu.Unlock()
			}
		}()
		var rows [][]float64
		_ = rows[3] // паника: выход за границы
	}()
	wg.Wait()

	require.Len(t, got, 1, "паника обязана стать сообщением, а не смертью процесса")
	assert.True(t, strings.HasPrefix(got[0], "table: "), "в сообщении должно быть видно место: %q", got[0])
	assert.Contains(t, got[0], "index out of range", "причина обязана сохраниться")
}
