package osrm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"ariadne/internal/geo"
)

// tableChunk — сколько пар кладём в один запрос /table.
// Матрица получается 2N×2N (начала и концы), то есть 300×300 при N=150.
// Считать лишнее дешевле, чем слать сотни отдельных запросов: нам нужна только
// диагональ, но матрица приходит целиком одним ответом.
const tableChunk = 150

// Pair — пара точек, между которыми нужно расстояние по дорогам.
type Pair struct {
	A, B geo.Point
}

// tableResponse — матрица расстояний.
type tableResponse struct {
	Code      string      `json:"code"`
	Distances [][]float64 `json:"distances"`
}

// routeResponse — маршрут между двумя точками.
type routeResponse struct {
	Code   string `json:"code"`
	Routes []struct {
		Distance float64 `json:"distance"`
		Duration float64 `json:"duration"`
		Geometry struct {
			Coordinates [][2]float64 `json:"coordinates"`
		} `json:"geometry"`
	} `json:"routes"`
	Waypoints []struct {
		Location [2]float64 `json:"location"`
	} `json:"waypoints"`
}

// PairDistance возвращает длину пути по дорогам для каждой пары, в метрах.
//
// Второе значение — признак «ответ получен». false означает, что проезда нет
// или сервер промолчал; ноль там был бы враньём.
//
// Берём МЕНЬШЕЕ из двух направлений. Физически путь по дорогам симметричен, а
// разница в разы — artefact привязки к разделённой трассе: точка со снэпом
// 3.7 м легла на встречную полосу, и OSRM повёл разворот через развязку —
// 71.1 км вместо 23.8 км в обратную сторону. Из-за этого проверка объявляла
// переход невозможным и выбрасывала три десятка честных точек, лежащих ровно
// на трассе.
func (c *Client) PairDistance(ctx context.Context, pairs []Pair) (dist []float64, ok []bool, warnings []string) {
	dist = make([]float64, len(pairs))
	ok = make([]bool, len(pairs))
	if len(pairs) == 0 {
		return dist, ok, nil
	}

	if c.useTable.Load() {
		warn := c.pairsByTable(ctx, pairs, dist, ok)
		warnings = append(warnings, warn...)
		// Если /table оказался закрыт, клиент это запомнил (см. pairsByTable)
		// и дальше идём вторым способом — по одной паре.
		if !c.useTable.Load() {
			for i := range ok {
				ok[i] = false
			}
			dist = make([]float64, len(pairs))
			warnings = append(warnings, "pairs: /table is closed, fell back to /route")
		} else {
			return dist, ok, warnings
		}
	}

	warn := c.pairsByRoute(ctx, pairs, dist, ok)
	return dist, ok, append(warnings, warn...)
}

// pairsByTable — матричный способ. Один запрос на 150 пар.
//
// Нужен там, где до OSRM далеко: на боевом сервере один запрос стоит 88 мс, и
// 29 тысяч отдельных запросов заняли бы шесть минут против двенадцати секунд
// матрицами. Если сервер отвечает 404 (ручка закрыта), способ отключается
// насовсем и клиент переходит на пары.
func (c *Client) pairsByTable(ctx context.Context, pairs []Pair, dist []float64, ok []bool) []string {
	type chunk struct{ lo, hi int }
	var chunks []chunk
	for lo := 0; lo < len(pairs); lo += tableChunk {
		chunks = append(chunks, chunk{lo, min(lo+tableChunk, len(pairs))})
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		warn []string
	)
	for _, ch := range chunks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					warn = append(warn, panicErr(ctx, "table", r).Error())
					mu.Unlock()
				}
			}()
			if err := ctx.Err(); err != nil {
				return
			}
			part := pairs[ch.lo:ch.hi]
			m := len(part)

			buf := make([]byte, 0, 32*m+64)
			buf = append(buf, "/table/v1/driving/"...)
			for i, p := range part {
				if i > 0 {
					buf = append(buf, ';')
				}
				buf = appendCoord(buf, p.A.Lon, p.A.Lat)
			}
			for _, p := range part {
				buf = append(buf, ';')
				buf = appendCoord(buf, p.B.Lon, p.B.Lat)
			}
			buf = append(buf, "?annotations=distance"...)

			body, err := c.get(ctx, string(buf))
			if err != nil {
				var he *httpError
				if errors.As(err, &he) && he.Code == http.StatusNotFound {
					// Ручка закрыта — на этом сервере она недоступна вообще,
					// пробовать её на следующих запросах незачем.
					c.useTable.Store(false)
				}
				mu.Lock()
				warn = append(warn, fmt.Sprintf("table: %v", err))
				mu.Unlock()
				return
			}

			var r tableResponse
			if err := json.Unmarshal(body, &r); err != nil || r.Code != "Ok" {
				mu.Lock()
				warn = append(warn, fmt.Sprintf("table: malformed response: %v", err))
				mu.Unlock()
				return
			}
			if bad := matrixShape(r.Distances, m); bad != "" {
				mu.Lock()
				warn = append(warn, "table: "+bad)
				mu.Unlock()
				return
			}

			for i := range part {
				fwd := r.Distances[i][m+i] // из начала i в конец i
				rev := r.Distances[m+i][i] // и обратно
				v, has := minValid(fwd, rev)
				if has {
					dist[ch.lo+i] = v
					ok[ch.lo+i] = true
				}
			}
		}()
	}
	wg.Wait()
	return warn
}

// matrixShape проверяет, что из матрицы можно читать диагональ начал и концов.
// Возвращает описание беды или пустую строку, если форма годная.
//
// Читаем мы клетки `[i][m+i]` и `[m+i][i]` для каждой пары i, то есть трогаем
// все 2m строк и в каждой доходим до столбца 2m-1. Значит и требовать надо
// именно это: 2m строк, и в КАЖДОЙ не меньше 2m чисел.
//
// Проверять одну высоту мало, и это не теория. Матрица нужной высоты с
// короткими строками приходит от балансировщика, отдавшего чужой ответ, и от
// OSRM, которого позвали с `sources`/`destinations`: строк столько, сколько
// просили, а чисел в строке — по числу целей. Обращение за конец строки роняло
// не задачу, а весь процесс: паника случается в дочерней горутине, куда
// `recover` воркера не достаёт.
//
// Строки за пределами первых 2m не смотрим: лишние клетки нам не мешают.
func matrixShape(rows [][]float64, m int) string {
	if len(rows) < 2*m {
		return fmt.Sprintf("matrix has %d rows instead of %d", len(rows), 2*m)
	}
	for i := range 2 * m {
		if len(rows[i]) < 2*m {
			return fmt.Sprintf("matrix row %d has %d numbers instead of %d",
				i, len(rows[i]), 2*m)
		}
	}
	return ""
}

// pairsByRoute — по одному запросу на направление.
//
// Медленнее матриц, когда до сервера далеко, но работает везде: /route открыт
// всегда. На своём OSRM, стоящем рядом, этот способ даже быстрее матричного —
// упирается не в число запросов, а в счёт самого движка.
func (c *Client) pairsByRoute(ctx context.Context, pairs []Pair, dist []float64, ok []bool) []string {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed int
		first  string
	)

	// Пул фиксированного размера, а не горутина на пару: пар бывает под
	// пятнадцать тысяч, и заводить столько горутин ради ожидания в очереди
	// к семафору — впустую занятая память. Работников чуть больше, чем мест
	// в семафоре, чтобы освободившееся место занималось сразу.
	workers := min(cap(c.sem)+4, len(pairs))
	jobs := make(chan int)

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					if first == "" {
						first = panicErr(ctx, "route", r).Error()
					}
					mu.Unlock()
				}
			}()
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					return
				}
				p := pairs[i]
				fwd, ferr := c.routeDistance(ctx, p.A, p.B)
				rev, rerr := c.routeDistance(ctx, p.B, p.A)

				var v float64
				has := false
				if ferr == nil {
					v, has = fwd, true
				}
				if rerr == nil && (!has || rev < v) {
					v, has = rev, true
				}
				if !has {
					mu.Lock()
					failed++
					if first == "" && ferr != nil {
						first = ferr.Error()
					}
					mu.Unlock()
					continue
				}
				dist[i] = v
				ok[i] = true
			}
		}()
	}

	for i := range pairs {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return []string{fmt.Sprintf("pairs: interrupted at pair %d of %d", i, len(pairs))}
		}
	}
	close(jobs)
	wg.Wait()

	if failed > 0 {
		return []string{fmt.Sprintf("pairs: no route for %d of %d pairs (%s)",
			failed, len(pairs), first)}
	}
	return nil
}

// routeDistance — длина пути по дорогам между двумя точками, без геометрии.
func (c *Client) routeDistance(ctx context.Context, a, b geo.Point) (float64, error) {
	buf := make([]byte, 0, 96)
	buf = append(buf, "/route/v1/driving/"...)
	buf = appendCoord(buf, a.Lon, a.Lat)
	buf = append(buf, ';')
	buf = appendCoord(buf, b.Lon, b.Lat)
	buf = append(buf, "?overview=false&steps=false"...)

	body, err := c.get(ctx, string(buf))
	if err != nil {
		return 0, err
	}
	var r routeResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, fmt.Errorf("osrm: parse route: %w", err)
	}
	if r.Code != "Ok" || len(r.Routes) == 0 {
		return 0, fmt.Errorf("osrm: route code %q", r.Code)
	}
	return r.Routes[0].Distance, nil
}

// minValid — меньшее из двух, пропуская отсутствующие значения.
// В матрице /table «нет пути» приходит как null, что в Go даёт ноль; отличить
// его от настоящего нуля нельзя, поэтому ноль здесь считаем отсутствием —
// расстояние между двумя разными точками нулевым не бывает.
func minValid(a, b float64) (float64, bool) {
	switch {
	case a > 0 && b > 0:
		return min(a, b), true
	case a > 0:
		return a, true
	case b > 0:
		return b, true
	default:
		return 0, false
	}
}
