// Package service — прослойка между транспортом и конвейером: собирает
// параметры прогона из конфига, проверяет потолки, запускает чистку и считает
// километраж до и после.
//
// Существует ради того, чтобы HTTP, gRPC и debug-ручка делали это одинаково.
// Без неё каждая из трёх точек входа собирала бы параметры сама, и они
// незаметно разъехались бы.
package service

import (
	"context"
	"errors"

	"ariadne/internal/config"
	"ariadne/internal/geo"
	"ariadne/internal/logger"
	"ariadne/internal/pipeline"
)

var (
	// ErrTooManyPoints — во входе больше точек, чем разрешает MAX_POINTS.
	// Проверяется ДО чистки: на негодный вход не тратится ни один поход к
	// маршрутизатору.
	ErrTooManyPoints = errors.New("too many points")

	// ErrTooFewPoints — после чистки не осталось даже пары точек, то есть
	// маршрута нет. Отдаётся как ошибка, а не как пустой результат с нулевой
	// длиной: ноль километров выглядит настоящим ответом и уйдёт в отчёт.
	ErrTooFewPoints = errors.New("too few points after processing")
)

// Result — всё, что известно о прогоне: сам очищенный маршрут, его длина и
// то, по чему потом разбирают спорный случай.
//
// BeforeLenMeters и BeforeCount держатся ради сравнения: «было 9568 км, стало
// 2871» — первое, что спрашивают, когда километраж не сошёлся с ожиданием.
type Result struct {
	Points []geo.Point

	// Synthetic — какие точки выдуманы дорисовкой по дорожной сети, по одной
	// пометке на точку. Их не было во входе, и времена на них разложены по
	// доле пути — потребитель обязан уметь их отличить.
	Synthetic       []bool
	LengthMeters    float64
	BeforeLenMeters float64
	BeforeCount     int
	Warnings        []string
	Stats           []pipeline.StageStats

	// Degraded — результат неполный: конвейер знает, что мог бы лучше, но не
	// смог (кончился бюджет, не задан маршрутизатор). Километраж при этом
	// занижен. Отдавать такое молча нельзя — цифра уходит в деньги.
	Degraded bool
}

// Service — сервис чистки. Состояния между запросами не держит: конвейер
// собирается заново на каждый прогон.
type Service struct {
	cfg    *config.Config
	router pipeline.Router
}

// New собирает сервис. `router` может быть nil: без маршрутизатора чистка и
// дорисовка пропускают трек насквозь, сервис продолжает работать.
func New(cfg *config.Config, router pipeline.Router) *Service {
	return &Service{cfg: cfg, router: router}
}

// Resolve чистит маршрут: проверяет потолок точек, гоняет конвейер, меряет
// длину до и после.
//
// Дедлайн задаётся через ctx вызывающим. Исчерпан он или нет, решает конвейер:
// на исчерпанном он либо возвращает ошибку, либо помечает результат неполным —
// молча обрезанного трека наружу не уходит.
func (s *Service) Resolve(ctx context.Context, points []geo.Point) (*Result, error) {
	if len(points) > s.cfg.MaxPoints {
		return nil, ErrTooManyPoints
	}

	beforeCount := len(points)
	beforeMeters := geo.TotalLength(points)

	params := s.buildParams()
	pl := pipeline.New(params, s.router)

	cleaned, warnings, stats, err := pl.Run(ctx, points)
	if err != nil {
		// Статистику по уже пройденным стадиям не теряем: по ней видно, где
		// именно сломалось, и это первое, что спрашивают при разборе.
		logger.FromContext(ctx).Error("pipeline failed",
			"error", err, "stages", len(stats), "stats", stats)
		return nil, err
	}

	if len(cleaned) < 2 {
		return nil, ErrTooFewPoints
	}

	st := pl.State()
	return &Result{
		Synthetic:       st.Synthetic,
		Degraded:        st.Degraded,
		Points:          cleaned,
		LengthMeters:    geo.TotalLength(cleaned),
		BeforeLenMeters: beforeMeters,
		BeforeCount:     beforeCount,
		Warnings:        warnings,
		Stats:           stats,
	}, nil
}

func (s *Service) buildParams() pipeline.Params {
	return pipeline.Params{
		DedupDistanceMeters: s.cfg.DedupDistanceMeters,
		DedupTimeGap:        s.cfg.DedupTimeGap,
		StopRadiusMeters:    s.cfg.StopRadiusMeters,
		StopMinPoints:       s.cfg.StopMinPoints,
		SimplifyMinMeters:   s.cfg.SimplifyMinMeters,
	}
}
