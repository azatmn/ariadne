package service

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"ariadne/internal/config"
	"ariadne/internal/osrm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Настройки клиента OSRM собираются здесь, и собираются ПОЛНОСТЬЮ.
//
// Неперенесённое поле не ломает сборку и не видно в логе — оно молча остаётся
// нулевым. Так уже вышло с повторами: код повторов написан, но приезжал ноль,
// и любой моргнувший запрос считался отказом навсегда.

func TestOSRMConfig_CarriesEverything(t *testing.T) {
	cfg := &config.Config{
		OSRMURL:         "http://osrm:5000",
		OSRMTimeout:     7 * time.Second,
		OSRMMaxParallel: 5,
		OSRMRetries:     4,
	}

	got := osrmConfigOf(cfg)

	assert.Equal(t, "http://osrm:5000", got.BaseURL)
	assert.Equal(t, 7*time.Second, got.RequestTimeout)
	assert.Equal(t, 5, got.MaxParallel)
	assert.Equal(t, 4, got.Retries, "повторы обязаны доехать: с нулём их нет вовсе")
	assert.Equal(t, osrm.TableAuto, got.UseTable,
		"режим матриц задаём явно, а не полагаемся на нулевое значение типа")
}

func TestNewRouter_WithoutURL(t *testing.T) {
	// Пустой адрес — рабочее состояние, а не ошибка: сервис поднимается,
	// чистка и дорисовка пропускают трек с предупреждением.
	r := NewRouter(&config.Config{}, testLogger())
	assert.Nil(t, r, "без адреса маршрутизатора быть не должно")
}

func TestNewRouter_BuildsClient(t *testing.T) {
	// Адрес задан — клиент собран. Связь при этом не проверяется здесь:
	// её проверяет Ping, и недоступный OSRM не мешает сервису подняться.
	cfg := &config.Config{
		OSRMURL:         "http://127.0.0.1:1",
		OSRMTimeout:     time.Second,
		OSRMMaxParallel: 2,
		OSRMRetries:     1,
	}

	r := NewRouter(cfg, testLogger())
	require.NotNil(t, r, "с адресом клиент обязан собраться")
}

func TestNewRouter_EmptyURLIsWarningNotError(t *testing.T) {
	// Незаданный адрес — это осознанная настройка, а не поломка. В логе он
	// обязан быть ПРЕДУПРЕЖДЕНИЕМ: если писать ошибку, мониторинг будет
	// кричать на штатное состояние, и на него перестанут смотреть.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	r := NewRouter(&config.Config{}, logger)
	require.Nil(t, r)

	out := buf.String()
	assert.Contains(t, out, "level=WARN", "пустой адрес — предупреждение")
	assert.NotContains(t, out, "level=ERROR", "и точно не ошибка")
	assert.Contains(t, out, "OSRM_URL is empty", "и сказать надо понятно")
}
