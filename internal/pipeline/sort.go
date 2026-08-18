package pipeline

import (
	"context"
	"slices"

	"ariadne/internal/geo"
)

// SortByTime — первая стадия: раскладывает точки по времени.
//
// Трекеры шлют точки не по порядку: буфер выгружается пачкой после потери
// связи и приезжает позже свежих. Весь дальнейший конвейер считает соседей по
// списку соседями по времени, поэтому сортировка обязана идти первой.
//
// Сортировка УСТОЙЧИВАЯ: у точек с одинаковой посекундной меткой порядок
// остаётся тем, в каком они пришли. Внутри такой пачки настоящего порядка не
// существует, и выбирает его потом ядро — но выбирать оно должно от одного и
// того же начала, иначе результат гуляет от прогона к прогону.
//
// Вход не меняется: сортируется копия. Вызывающий отдал свой срез и вправе
// рассчитывать, что тот остался прежним.
type SortByTime struct{}

func (SortByTime) Name() string { return "sort_by_time" }

func (SortByTime) Apply(_ context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	sorted := slices.Clone(points)
	slices.SortStableFunc(sorted, func(a, b geo.Point) int {
		return a.Time.Compare(b.Time)
	})
	return sorted, nil, nil
}
