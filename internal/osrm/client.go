// Package osrm — клиент дорожного сервиса OSRM.
//
// Алгоритм очистки спрашивает у OSRM две вещи: насколько каждая точка отстоит
// от ближайшей дороги («снэп») и сколько между двумя точками по дорогам. Первое
// даёт вес точке — далеко от дороги значит подозрительно; второе проверяет, был
// ли переход между точками физически возможен.
//
// Запросов много: на треке в 13 тысяч точек — около 14 тысяч пар. Поэтому цена
// одного запроса и число одновременных запросов решают, уложимся ли мы в бюджет
// задачи. Замер боевого сервера показал, что он выдерживает около 75 запросов
// в секунду и при 64 одновременных начинает захлёбываться — отсюда ограничитель
// внутри клиента, а не снаружи.
package osrm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ErrNotConfigured — адрес OSRM не задан. Возвращается при создании клиента,
// чтобы сервис падал на старте с понятной причиной, а не на первой задаче.
var ErrNotConfigured = errors.New("osrm: base url is empty")

// Config — настройки клиента. Заполняется из переменных окружения.
type Config struct {
	// BaseURL — адрес сервиса, например https://osrm.trucks.ru
	BaseURL string

	// MaxParallel — сколько запросов держим в полёте одновременно.
	// Это потолок на ВЕСЬ сервис, а не на одну задачу: воркеров четыре, и без
	// общего ограничителя они вчетвером положат OSRM, после чего деградируют
	// сразу все задачи, а не одна.
	MaxParallel int

	// BatchPoints — сколько точек кладём в один запрос снэпов.
	// Боевой сервер принимает 400 и отвечает 414 на 1000 — упирается в длину
	// адреса. Значение уменьшается на ходу, если сервер всё же ответил 414.
	BatchPoints int

	// RequestTimeout — потолок на один запрос. Общий бюджет задачи приходит
	// отдельно, через ctx, и всегда главнее: ждать дольше, чем осталось
	// у задачи, бессмысленно.
	RequestTimeout time.Duration

	// Retries — сколько раз повторяем запрос, который не удался по временной
	// причине (сеть, 5xx, таймаут). Отказы по существу (400, 404, 414)
	// не повторяются никогда — ответ от этого не изменится.
	Retries int

	// UseTable — пользоваться ли матричной ручкой /table для расстояний.
	//
	//	TableAuto  пробовать; если сервер ответит 404, запомнить и больше
	//	           не пробовать — так закрытая ручка выясняется сама;
	//	TableOn    только матрицы (быстрее, когда до сервера далеко);
	//	TableOff   только пары (работает везде, /route открыт всегда).
	UseTable TableMode
}

// TableMode — как пользоваться ручкой /table.
type TableMode string

const (
	TableAuto TableMode = "auto"
	TableOn   TableMode = "true"
	TableOff  TableMode = "false"
)

// Client — клиент OSRM. Безопасен для одновременного использования.
type Client struct {
	baseURL string
	http    *http.Client
	timeout time.Duration
	retries int

	// sem — ограничитель одновременных запросов. Обычный буферизованный канал:
	// место в буфере есть — идём, нет — ждём, но не дольше, чем живёт ctx.
	sem chan struct{}

	// batch — текущий размер батча снэпов. Меняется на ходу при ответе 414,
	// поэтому atomic: клиент общий для всех воркеров.
	batch atomic.Int64

	// useTable — доступна ли матричная ручка. Гасится навсегда, если сервер
	// ответил 404: закрытая ручка сама себя не откроет, а пробовать её на
	// каждом запросе значит платить лишний круг к серверу за каждый трек.
	useTable atomic.Bool
}

// New создаёт клиента. Ошибку возвращает только при негодной настройке —
// недоступность самого OSRM выясняется позже, при первом запросе.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, ErrNotConfigured
	}
	if cfg.MaxParallel < 1 {
		cfg.MaxParallel = 16
	}
	if cfg.BatchPoints < 2 {
		cfg.BatchPoints = 400
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	}

	// Транспорт настраиваем явно, и это не украшательство. По умолчанию Go
	// держит два свободных соединения на хост, поэтому при нашей параллельности
	// почти каждый запрос начинается с установки TLS-соединения заново. Замер
	// боевого сервера: 319 мс на запрос без переиспользования против 88 мс с
	// переиспользованием — разница втрое на ровном месте.
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          cfg.MaxParallel * 2,
		MaxIdleConnsPerHost:   cfg.MaxParallel,
		MaxConnsPerHost:       cfg.MaxParallel,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	c := &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		http:    &http.Client{Transport: tr},
		timeout: cfg.RequestTimeout,
		retries: cfg.Retries,
		sem:     make(chan struct{}, cfg.MaxParallel),
	}
	c.batch.Store(int64(cfg.BatchPoints))
	c.useTable.Store(cfg.UseTable != TableOff)
	return c, nil
}

// BatchPoints — текущий размер батча снэпов.
func (c *Client) BatchPoints() int { return int(c.batch.Load()) }

// UsesTable — пользуется ли клиент матричной ручкой прямо сейчас.
// В режиме auto ответ меняется после первого же 404 от сервера.
func (c *Client) UsesTable() bool { return c.useTable.Load() }

// shrinkBatch уменьшает батч вдвое после ответа 414 (адрес слишком длинный).
// Лимит длины адреса на разных установках OSRM разный, и подбирать его
// вручную под каждую — тупик; сервер сам скажет, когда перебор.
func (c *Client) shrinkBatch() int {
	for {
		cur := c.batch.Load()
		if cur <= 50 {
			return int(cur) // ниже не опускаемся: дробить до бесконечности бессмысленно
		}
		next := cur / 2
		if c.batch.CompareAndSwap(cur, next) {
			return int(next)
		}
	}
}

// httpError — ответ сервера с кодом, который мы не считаем успехом.
type httpError struct {
	Code int
	Body string
}

// Error — текст с кодом и телом ответа. Тело оставлено целиком намеренно:
// OSRM пишет в него причину («NoRoute», «TooBig»), и без неё по одному коду
// не разобрать, что случилось.
func (e *httpError) Error() string {
	return fmt.Sprintf("osrm: http %d: %s", e.Code, e.Body)
}

// permanent сообщает, что повторять запрос незачем: ответ не изменится.
//   - 400 OSRM отдаёт на «маршрута нет» и на негодные координаты;
//   - 404 — ручка закрыта (так боевой отвечает на /table и /nearest);
//   - 414 — слишком длинный адрес, лечится уменьшением батча, а не повтором.
func (e *httpError) permanent() bool {
	return e.Code == http.StatusBadRequest ||
		e.Code == http.StatusNotFound ||
		e.Code == http.StatusRequestURITooLong
}

// get выполняет запрос и возвращает тело ответа.
//
// Три вещи, которые здесь важны:
//
//	ОГРАНИЧИТЕЛЬ  ждём место в семафоре, но не дольше, чем живёт ctx;
//	ДЕДЛАЙН       на запрос ставим свой потолок, но общий бюджет задачи главнее;
//	ПОВТОРЫ       только на временных отказах, и пауза перед повтором тоже
//	              обрезается остатком ctx — спать дольше, чем живёт задача,
//	              значит гарантированно вернуться ни с чем.
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	var lastErr error

	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		body, err := c.once(ctx, path)
		if err == nil {
			return body, nil
		}
		lastErr = err

		var he *httpError
		if errors.As(err, &he) && he.permanent() {
			return nil, err
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if attempt >= c.retries {
			return nil, lastErr
		}

		// Пауза растёт с номером попытки, плюс случайная добавка: без неё
		// сотня запросов, упавших разом, повторится тоже разом и добьёт сервер.
		wait := time.Duration(1<<attempt) * 200 * time.Millisecond
		wait += time.Duration(rand.Int64N(int64(wait / 2)))
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// once — одна попытка: занять место в семафоре, сходить, прочитать ответ.
func (c *Client) once(ctx context.Context, path string) ([]byte, error) {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("osrm: build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Без адреса. Go кладёт в `*url.Error` весь URL, а у нас в нём сотни
		// координат батча: одна строка лога уезжала на килобайты, забивала
		// собой всё вокруг и выносила наружу сам трек. Причина («connection
		// refused», «timeout») сохраняется — она и нужна.
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("osrm: request: %w", ue.Err)
		}
		return nil, fmt.Errorf("osrm: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Тело читаем всегда, даже при ошибочном коде: во-первых, в нём объяснение
	// от OSRM, во-вторых, недочитанное тело не даёт переиспользовать соединение,
	// а ради этого переиспользования и настраивался транспорт.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("osrm: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		msg := string(body)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, &httpError{Code: resp.StatusCode, Body: msg}
	}
	return body, nil
}

// appendCoord дописывает координату в формате OSRM: «долгота,широта».
//
// Пять знаков после запятой — это около 1.1 метра, и ровно столько шлёт прототип.
// Значение здесь не косметическое: снэп считается от того, что мы отправили,
// поэтому иная точность дала бы иные веса и иной итог. Меняя её, надо заново
// прогонять сверку с прототипом.
func appendCoord(dst []byte, lon, lat float64) []byte {
	dst = strconv.AppendFloat(dst, lon, 'f', 5, 64)
	dst = append(dst, ',')
	dst = strconv.AppendFloat(dst, lat, 'f', 5, 64)
	return dst
}
