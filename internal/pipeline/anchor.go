package pipeline

import (
	"context"

	"ariadne/internal/geo"
)

// RemoveAnchorBacktrack — якорный фильтр (способ 2 + способ 1).
// Якоря = первая (старт) и последняя (цель) точка сегмента: после сортировки это
// IN/OUT, гарантированно правильные (~50 м от app_point). На отрезке старт→цель
// легитимная точка ОДНОВРЕМЕННО удаляется от старта И приближается к цели.
//
// Способ 2 (ядро): точка — глюк, если относительно предыдущей оставленной она
// дёрнулась НАЗАД к старту И ОТ цели сразу. Меряем прогресс по двум якорям, а НЕ
// отклонение от прямой — поэтому кривизна дорог не мешает.
//
// Способ 1 (страховка): срабатываем только при ЗНАЧИТЕЛЬНОМ откате (> порога),
// мелкие колебания от изгибов дорог игнорируем.
//
// Боковые/спуфинг-кружения (нарушают только одно условие) — не наша работа,
// их добивает RemoveTeleports. ToleranceMeters <= 0 → стадия выключена.
//
// СТАДИЯ СНЯТА С КОНВЕЙЕРА. В pipeline.New не включается; файл и тесты
// оставлены как след разобранного подхода. Причина снятия общая для всех
// четырёх прежних фильтров: они судят по одному признаку и удаляют сразу,
// а ошибка такого удаления необратима — выброшенная точка становится опорой
// для следующей проверки, и за одним глюком уезжает честный кусок трека.
// Сейчас это делает ядро (пакет core): правила только копят штрафы, а
// выбрасывает один раз общий выбор цепочки, взвесив все улики разом.
//
// Замер, стоивший ей места: на рабочей зоне в Бутове якорный фильтр стирал
// настоящую стоянку целиком — машина там кружит по территории, то есть по его
// меркам «откатывается от цели» весь день.
type RemoveAnchorBacktrack struct {
	ToleranceMeters float64
}

func (RemoveAnchorBacktrack) Name() string { return "remove_anchor_backtrack" }

func (f RemoveAnchorBacktrack) Apply(_ context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	if f.ToleranceMeters <= 0 || len(points) < 3 {
		return points, nil, nil // выключено или нет промежуточных точек
	}

	start := points[0]
	target := points[len(points)-1]

	result := make([]geo.Point, 0, len(points))
	result = append(result, start) // якорь-старт

	prev := start // предыдущая ОСТАВЛЕННАЯ точка (опора для сравнения)
	for i := 1; i < len(points)-1; i++ {
		p := points[i]
		towardStart := geo.Haversine(prev, start) - geo.Haversine(p, start)  // >0: ближе к старту, чем prev
		awayTarget := geo.Haversine(p, target) - geo.Haversine(prev, target) // >0: дальше от цели, чем prev

		if towardStart > f.ToleranceMeters && awayTarget > f.ToleranceMeters {
			continue // откат по ОБЕИМ дистанциям больше порога → глюк
		}
		result = append(result, p)
		prev = p
	}
	result = append(result, target) // якорь-цель

	return result, nil, nil
}
