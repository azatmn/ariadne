package service

import (
	"context"
	"errors"

	"ariadne/internal/config"
	"ariadne/internal/geo"
	"ariadne/internal/pipeline"
)

var (
	ErrTooManyPoints = errors.New("too many points")
	ErrTooFewPoints  = errors.New("too few points after processing")
)

type Result struct {
	Points          []geo.Point
	LengthMeters    float64
	BeforeLenMeters float64
	BeforeCount     int
	Warnings        []string
	Stats           []pipeline.StageStats
}

type Service struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Service {
	return &Service{cfg: cfg}
}

func (s *Service) Resolve(ctx context.Context, points []geo.Point) (*Result, error) {
	if len(points) > s.cfg.MaxPoints {
		return nil, ErrTooManyPoints
	}

	beforeCount := len(points)
	beforeMeters := geo.TotalLength(points)

	params := s.buildParams()
	pl := pipeline.New(params)

	cleaned, warnings, stats, err := pl.Run(ctx, points)
	if err != nil {
		return nil, err
	}

	if len(cleaned) < 2 {
		return nil, ErrTooFewPoints
	}

	return &Result{
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
		IntersectMaxIter:    s.cfg.IntersectMaxIter,
		MaxSpeedKmh:         s.cfg.MaxSpeedKmh,
		MaxAccelKmhPerSec:   s.cfg.MaxAccelKmhPerSec,
		MaxLoopMeters:       s.cfg.MaxLoopMeters,
		MaxLoopSeconds:      s.cfg.MaxLoopSeconds,
		SimplifyMinMeters:   s.cfg.SimplifyMinMeters,
	}
}
