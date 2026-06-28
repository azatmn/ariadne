// Package callback уведомляет внешнюю систему (Laravel) о завершении задачи:
// POST на CALLBACK_URL с подстановкой {taskKey}. Тело — минимальный JSON
// {taskKey,status}; основной сигнал — сам taskKey в URL, по нему Laravel идёт
// в GET /v1/tasks/{taskKey} за результатом.
package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const placeholder = "{taskKey}"

// baseBackoff — базовая пауза между ретраями (умножается на номер попытки).
// var, а не const, чтобы тесты могли её занижать.
var baseBackoff = 200 * time.Millisecond

// Client шлёт коллбэки. Пустой urlTemplate означает «коллбэки выключены».
type Client struct {
	urlTemplate string
	retries     int
	http        *http.Client
	logger      *slog.Logger
}

// New собирает клиент. timeout — на один HTTP-запрос; retries — число повторов
// сверх первой попытки.
func New(urlTemplate string, retries int, timeout time.Duration, logger *slog.Logger) *Client {
	return &Client{
		urlTemplate: urlTemplate,
		retries:     retries,
		http:        &http.Client{Timeout: timeout},
		logger:      logger,
	}
}

type payload struct {
	TaskKey string `json:"taskKey"`
	Status  string `json:"status"`
}

// Notify шлёт POST на CALLBACK_URL. Ретраит на сетевую ошибку и 5xx; на 4xx
// сдаётся сразу (Laravel явно отверг). При пустом urlTemplate — no-op.
func (c *Client) Notify(ctx context.Context, taskKey, status string) error {
	if c.urlTemplate == "" {
		return nil // коллбэки выключены
	}

	url := strings.ReplaceAll(c.urlTemplate, placeholder, taskKey)
	body, err := json.Marshal(payload{TaskKey: taskKey, Status: status})
	if err != nil {
		return fmt.Errorf("callback: marshal payload: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(baseBackoff * time.Duration(attempt)):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("callback: build request: %w", err) // кривой URL — ретрай не поможет
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue // сетевая ошибка → ретрай
		}
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode < 300:
			return nil // успех
		case resp.StatusCode < 500:
			return fmt.Errorf("callback: %s returned %d", url, resp.StatusCode) // 4xx — не ретраим
		default:
			lastErr = fmt.Errorf("callback: %s returned %d", url, resp.StatusCode) // 5xx → ретрай
		}
	}
	return fmt.Errorf("callback: all attempts failed: %w", lastErr)
}
