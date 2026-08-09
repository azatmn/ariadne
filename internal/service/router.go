package service

import (
	"context"
	"log/slog"

	"ariadne/internal/config"
	"ariadne/internal/osrm"
	"ariadne/internal/pipeline"
)

// NewRouter поднимает клиент маршрутизатора по настройкам сервиса.
//
// Возвращает nil, если адрес не задан: сервис обязан работать и без OSRM —
// чистка и дорисовка тогда пропускают трек насквозь и предупреждают об этом.
//
// Связь проверяем сразу и громко. Сервис, который не может достучаться до
// маршрутизатора, должен сказать это в логе на старте, а не выяснять на первой
// же задаче, когда разбираться будет некому. Но НЕ падаем: OSRM может
// подняться позже, а задачи принимать надо уже сейчас.
func NewRouter(cfg *config.Config, logger *slog.Logger) pipeline.Router {
	if cfg.OSRMURL == "" {
		logger.Warn("OSRM_URL is empty: cleaning and gap filling disabled")
		return nil
	}

	client, err := osrm.New(osrmConfigOf(cfg))
	if err != nil {
		logger.Error("osrm client init failed", "url", cfg.OSRMURL, "error", err)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.OSRMTimeout)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		logger.Error("osrm unreachable", "url", cfg.OSRMURL, "error", err)
	} else {
		logger.Info("osrm connected", "url", cfg.OSRMURL)
	}
	return client
}

// osrmConfigOf переносит настройки сервиса в настройки клиента.
//
// Вынесено отдельно, чтобы проверять тестом: неперенесённое поле не ломает
// сборку и не видно в логе — оно молча остаётся нулевым. Так уже вышло с
// повторами.
func osrmConfigOf(cfg *config.Config) osrm.Config {
	return osrm.Config{
		BaseURL:        cfg.OSRMURL,
		MaxParallel:    cfg.OSRMMaxParallel,
		RequestTimeout: cfg.OSRMTimeout,
		Retries:        cfg.OSRMRetries,

		// Задаём явно, а не полагаемся на нулевое значение типа: сейчас пустая
		// строка случайно не равна TableOff и потому работает как «пробовать»,
		// но опираться на такое совпадение нельзя.
		//
		// Режим авто: матричная ручка /table на боевом сервере закрыта (404), а
		// на своём открыта. Клиент пробует её сам и гасит навсегда, если сервер
		// ответил 404. Разница огромная: 100×100 матрицей — 0.64 с, те же пары
		// поштучно — часы.
		UseTable: osrm.TableAuto,
	}
}
