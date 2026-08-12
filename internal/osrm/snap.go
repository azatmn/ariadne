package osrm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"ariadne/internal/geo"
)

// maxSplitDepth — до скольких раз дробим отказавший батч.
// Двенадцать делений превращают 400 точек в одну, дальше дробить нечего.
const maxSplitDepth = 12

// snapResponse — из ответа /route нам нужно только одно поле на точку.
//
// Маршрут (routes) мы намеренно не разбираем, хотя он в ответе есть. Причина в
// том, что внутри батча OSRM обязан проходить точки по правилам движения и на
// разделённых трассах вставляет развороты через развязку: тринадцать честных
// метров превращаются в пятьсот пятьдесят четыре. Расстояния по дорогам поэтому
// спрашиваются отдельно, парами (см. PairDistance).
type snapResponse struct {
	Code      string `json:"code"`
	Waypoints []struct {
		Distance float64 `json:"distance"`
	} `json:"waypoints"`
}

// Snap возвращает для каждой точки расстояние в метрах до ближайшей дороги.
//
// Второе значение — признак «ответ получен». Где false, там OSRM промолчал, и
// это НЕ ноль: ноль означал бы «точка лежит ровно на дороге», то есть сильный
// довод за неё. Молчание не довод ни за, ни против, и алгоритм обязан отличать
// одно от другого.
//
// Ошибку не возвращаем: частичный отказ — рабочая ситуация, из-за которой
// нельзя ронять всю задачу. Что не получилось, видно по ok и по warnings.
func (c *Client) Snap(ctx context.Context, pts []geo.Point) (snap []float64, ok []bool, warnings []string) {
	snap = make([]float64, len(pts))
	ok = make([]bool, len(pts))
	if len(pts) < 2 {
		return snap, ok, nil
	}

	// Батчи идут с перехлёстом в одну точку: последняя точка батча становится
	// первой в следующем. Иначе стык между батчами не проверяется никем.
	batch := c.BatchPoints()
	var queue []span
	for i := 0; i < len(pts)-1; {
		j := min(i+batch, len(pts))
		queue = append(queue, span{lo: i, hi: j})
		i = j - 1
	}

	var (
		mu       sync.Mutex
		failed   int
		firstErr string
	)

	// Волнами: сначала пробуем все диапазоны, отказавшие делим пополам и
	// пробуем снова. Так параллельность остаётся под контролем семафора,
	// а ветвление не порождает горутины без счёта.
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			warnings = append(warnings, fmt.Sprintf("snap: interrupted: %v", err))
			return snap, ok, warnings
		}

		var next []span
		var wg sync.WaitGroup
		results := make([][]float64, len(queue))
		errs := make([]error, len(queue))

		for k, sp := range queue {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Паника здесь стала бы ошибкой куска: его поделят пополам,
				// как любой неудавшийся батч, и в худшем случае точки уйдут
				// в «не удалось получить снэп». Дробление ограничено
				// `maxSplitDepth`, так что вечного круга не выйдет.
				defer func() {
					if r := recover(); r != nil {
						errs[k] = panicErr(ctx, "snap", r)
					}
				}()
				results[k], errs[k] = c.snapSpan(ctx, pts[sp.lo:sp.hi])
			}()
		}
		wg.Wait()

		for k, sp := range queue {
			if errs[k] == nil {
				for i, d := range results[k] {
					snap[sp.lo+i] = d
					ok[sp.lo+i] = true
				}
				continue
			}
			// Ответ 414 значит «адрес слишком длинный» — батч уменьшается
			// на будущее, а этот кусок всё равно надо разделить.
			var he *httpError
			if errors.As(errs[k], &he) && he.Code == 414 {
				c.shrinkBatch()
			}
			mu.Lock()
			if firstErr == "" {
				firstErr = errs[k].Error()
			}
			mu.Unlock()

			// Батч целиком не построился — обычно из-за одной точки, до
			// которой нет проезда. Делим пополам, чтобы потерять не всё,
			// а только виноватую половину.
			if sp.depth < maxSplitDepth && sp.hi-sp.lo > 2 {
				mid := (sp.lo + sp.hi) / 2
				next = append(next,
					span{lo: sp.lo, hi: mid + 1, depth: sp.depth + 1},
					span{lo: mid, hi: sp.hi, depth: sp.depth + 1})
			}
		}
		queue = next
	}

	// Считаем по факту: сколько точек осталось без ответа. Складывать размеры
	// провалившихся кусков нельзя — при дроблении половины ПЕРЕКРЫВАЮТСЯ
	// (`[lo, mid+1]` и `[mid, hi]` делят точку mid), и одни и те же точки
	// попадают в сумму по нескольку раз. На живом прогоне это дало «не удалось
	// получить снэп для 4736 точек из 2369» — число, которое само себя
	// опровергает и обесценивает всю строку.
	failed = 0
	for _, o := range ok {
		if !o {
			failed++
		}
	}

	if failed > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"snap: no snap for %d of %d points (%s)",
			failed, len(pts), firstErr))
	}
	return snap, ok, warnings
}

// span — диапазон точек [lo, hi) и сколько раз его уже делили.
type span struct {
	lo, hi, depth int
}

// snapSpan — один батч. Возвращает снэпы длиной ровно hi-lo.
func (c *Client) snapSpan(ctx context.Context, pts []geo.Point) ([]float64, error) {
	buf := make([]byte, 0, 16*len(pts)+64)
	buf = append(buf, "/route/v1/driving/"...)
	for i, p := range pts {
		if i > 0 {
			buf = append(buf, ';')
		}
		buf = appendCoord(buf, p.Lon, p.Lat)
	}
	buf = append(buf, "?overview=false&steps=false&annotations=false"...)

	body, err := c.get(ctx, string(buf))
	if err != nil {
		return nil, err
	}

	var r snapResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("osrm: parse snap: %w", err)
	}
	if r.Code != "Ok" {
		return nil, fmt.Errorf("osrm: snap code %q", r.Code)
	}
	if len(r.Waypoints) != len(pts) {
		return nil, fmt.Errorf("osrm: snap: expected %d points, got %d",
			len(pts), len(r.Waypoints))
	}

	out := make([]float64, len(pts))
	for i, w := range r.Waypoints {
		out[i] = w.Distance
	}
	return out, nil
}
