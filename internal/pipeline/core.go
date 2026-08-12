package pipeline

import (
	"context"

	"ariadne/internal/core"
	"ariadne/internal/geo"
	"ariadne/internal/osrm"
)

// Стадия ядра — тонкая обёртка над пакетом `core`.
//
// Вся чистка живёт там и проверяется без конвейера. Здесь только стыковка:
// ядро думает индексами, конвейер обменивается точками; и то, что ядро узнало
// по дороге, надо передать следующим стадиям.

// PointKey — как опознать точку между стадиями.
//
// Индексы для этого не годятся: каждая стадия удаляет точки, и после дедупа с
// упаковкой номера съезжают. Время и место остаются.
type PointKey struct {
	Unix     int64
	Lon, Lat float64
}

// KeyOf — ключ точки. Время берём в секундах: у `time.Time` внутри бывает
// показание монотонных часов, и сравнение целых значений на нём спотыкается.
func KeyOf(p geo.Point) PointKey {
	return PointKey{Unix: p.Time.Unix(), Lon: p.Lon, Lat: p.Lat}
}

// RunState — общий блокнот стадий одного прогона.
//
// Стадии обмениваются только точками, и этого достаточно, пока каждая судит
// по геометрии. Но ядро знает то, чего по геометрии не видно: где машина
// стояла. Упрощение обязано такие точки сохранить (схлопнутая стоянка лежит
// почти на прямой между въездом и выездом, и RDP снимает её как лишнюю, хотя
// за ней полчаса стоянки), а дорисовка — не проводить дорогу внутри стоянки.
//
// Блокнот создаётся в `New` на прогон и живёт ровно один прогон.
type RunState struct {
	// Must — точки, которые нельзя снимать по геометрии.
	Must map[PointKey]struct{}

	// Report — что ядро сделало с треком.
	Report core.Report

	// BeforePacking — трек, каким он вышел из ядра.
	//
	// Нужен стражу достижимости: упаковка судит по геометрии и может создать
	// переход, который физически не проехать. Сравнить не с чем, если не
	// помнить, что было до неё.
	BeforePacking []geo.Point

	// Guarded — сколько точек страж вернул обратно.
	Guarded int

	// Synthetic — пометки «точка выдумана по дорожной сети», по одной на
	// точку итогового трека. Заполняет дорисовка.
	//
	// Контракт с вызывающим (решение 2026-07-27, вариант Б): сервис
	// дорисовывает маршрут, и точки, которых не было во входе, обязаны быть
	// отличимы — времена на них выдуманы пропорционально длине пути.
	Synthetic []bool

	// Fill — что дорисовка сделала с треком.
	Fill FillReport

	// Degraded — «результат неполный, мы сами знаем, что могли лучше».
	//
	// Один признак на весь прогон, а не три флага по углам. Причин на сегодня
	// три: кончился бюджет у ядра, кончился у дорисовки, маршрутизатор не задан
	// вовсе. Складывать их обязан конвейер — иначе каждый следующий потребитель
	// будет собирать сумму сам, забудет слагаемое и молча отдаст заниженный
	// километраж за точный. Ровно это и происходило.
	//
	// НЕ считается неполнотой: дыра, которую дорисовка нашла и осознанно
	// отказалась закрывать (крюк втрое длиннее прямой, физически не проехать).
	// Там мы сделали всё, что могли честно сделать, и оговорка обесценила бы
	// признак.
	Degraded bool
}

// Core — стадия чистки. Без движка пропускает точки насквозь: сервис должен
// работать и тогда, когда ядро не настроено.
type Core struct {
	Engine *core.Core
	State  *RunState
}

func (Core) Name() string { return "core" }

// BudgetShare — доля оставшегося времени, которую забирает чистка.
// Остальное достаётся дорисовке: см. CoreBudgetShare.
func (Core) BudgetShare() float64 { return CoreBudgetShare }

func (c Core) Apply(ctx context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	if c.Engine == nil {
		// Трек уходит как пришёл: спуфинг не убран, километраж на дырах
		// занижен. Предупреждения мало — его можно не прочитать; признак
		// неполноты читается машиной.
		c.markDegraded()
		return points, []string{"core: движок не задан, чистка пропущена"}, nil
	}

	keep, rep, err := c.Engine.Run(ctx, points)
	if err != nil {
		return nil, nil, err
	}

	out := make([]geo.Point, len(keep))
	for k, i := range keep {
		out[k] = points[i]
	}

	if rep.Degraded {
		c.markDegraded()
	}

	if c.State != nil {
		// Блокнот описывает ОДИН прогон: пишем поверх, а не поверх прошлого.
		c.State.Report = rep
		c.State.BeforePacking = out
		c.State.Guarded = 0
		c.State.Must = make(map[PointKey]struct{}, len(rep.Stops))
		for _, i := range rep.Stops {
			c.State.Must[KeyOf(points[i])] = struct{}{}
		}
	}

	return out, nil, nil
}

// pairSource — то, что умеет отвечать про расстояния по дорогам парами
// в терминах пакета `osrm`.
type pairSource interface {
	PairDistance(ctx context.Context, pairs []osrm.Pair) ([]float64, []bool, []string)
}

// RoadFrom — переходник от клиента OSRM к тому, что ждёт ядро.
//
// Пары у них одинаковой формы, но разных типов, и это намеренно: `core` не
// знает про сеть, а `osrm` не знает про чистку. Переходник — единственное
// место, где они встречаются.
func RoadFrom(c pairSource) core.RoadClient { return roadAdapter{c} }

type roadAdapter struct{ inner pairSource }

func (r roadAdapter) PairDistance(ctx context.Context, pairs []core.Pair) ([]float64, []bool, []string) {
	if len(pairs) == 0 {
		return nil, nil, nil
	}
	conv := make([]osrm.Pair, len(pairs))
	for i, p := range pairs {
		conv[i] = osrm.Pair{A: p.A, B: p.B}
	}
	return r.inner.PairDistance(ctx, conv)
}

// markDegraded помечает прогон неполным. Отдельным методом, потому что
// блокнота может не быть: конвейер собирают и без него.
func (c Core) markDegraded() {
	if c.State != nil {
		c.State.Degraded = true
	}
}
