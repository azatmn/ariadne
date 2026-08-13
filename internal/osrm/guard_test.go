package osrm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// panicTransport роняет панику внутри HTTP-запроса.
//
// Так проверяется САМА обвязка от паник в дочерних горутинах, а не помощник в
// отдельности. Настоящего пути к панике в коде нет (и хорошо), поэтому паника
// заводится снаружи — подменой транспорта. Рабочий код при этом не меняется ни
// на строку: `http.Client` и так собирается из настроек.
//
// Зачем: обвязка стоит в трёх местах и держит обещание «паника валит задачу, а
// не сервис». Снимут её — без этих тестов никто не заметит до первого кривого
// ответа от живого OSRM, то есть до падения в бою.
type panicTransport struct{}

func (panicTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("boom inside http round trip")
}

func panicClient(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	c := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0 })
	c.http.Transport = panicTransport{}
	return c
}

// Снэпы: паника в дочерней горутине не должна убивать процесс.
func TestGuard_SnapSurvivesPanic(t *testing.T) {
	c := panicClient(t)

	var (
		ok    []bool
		warns []string
	)
	require.NotPanics(t, func() {
		_, ok, warns = c.Snap(context.Background(), track(8))
	}, "паника обязана остаться внутри горутины")

	for i := range ok {
		assert.False(t, ok[i], "точка %d не могла быть отвечена", i)
	}
	assert.NotEmpty(t, warns, "и обязана быть объяснена")
}

// Матрица: то же самое для /table.
func TestGuard_TablePairsSurvivePanic(t *testing.T) {
	c := panicClient(t)
	pts := track(3)

	var warns []string
	require.NotPanics(t, func() {
		_, _, warns = c.PairDistance(context.Background(),
			[]Pair{{A: pts[0], B: pts[1]}, {A: pts[1], B: pts[2]}})
	})
	assert.NotEmpty(t, warns)
}

// Пары по одной: и для /route, когда матрица выключена.
func TestGuard_RoutePairsSurvivePanic(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	c := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0; cfg.UseTable = TableOff })
	c.http.Transport = panicTransport{}

	pts := track(3)
	var ok []bool
	require.NotPanics(t, func() {
		_, ok, _ = c.PairDistance(context.Background(),
			[]Pair{{A: pts[0], B: pts[1]}, {A: pts[1], B: pts[2]}})
	})
	for i := range ok {
		assert.False(t, ok[i], "пара %d не могла быть отвечена", i)
	}
}
