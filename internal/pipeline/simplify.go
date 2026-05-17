package pipeline

import "ariadne/internal/geo"

// Simplify (опционально, пост-MVP) — упрощение маршрута алгоритмом
// Рамера-Дугласа-Пекера: убираем точки, отстоящие от хорды менее чем
// на SimplifyMinMeters. Сохраняет визуальную форму, уменьшает вес.
type Simplify struct {
	SimplifyMinMeters float64
}

func (Simplify) Name() string { return "simplify" }

// TODO: реализовать (классический рекурсивный RDP).
func (s Simplify) Apply(points []geo.Point) ([]geo.Point, []string, error) {
	return points, nil, nil
}
