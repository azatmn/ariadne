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
	ErrTooManyPoints = errors.New("too many points")
	ErrTooFewPoints  = errors.New("too few points after processing")
)

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
}

type Service struct {
	cfg    *config.Config
	router pipeline.Router
}

// New собирает сервис. `router` может быть nil: без маршрутизатора чистка и
// дорисовка пропускают трек насквозь, сервис продолжает работать.
func New(cfg *config.Config, router pipeline.Router) *Service {
	return &Service{cfg: cfg, router: router}
}

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

	return &Result{
		Synthetic:       pl.State().Synthetic,
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
