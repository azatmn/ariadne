# ariadne

## API

### POST /v1/routes/resolve-collisions

Основной эндпоинт. Принимает маршрут, прогоняет через pipeline очистки, возвращает результат.

**Запрос:**

```json
{
  "routeCompressed": "<base64(zlib(JSON))>",
  "returnDebug": false
}
```

- `routeCompressed` (обязательное) — маршрут в сжатом формате (см. раздел ниже)
- `returnDebug` (опциональное) — зарезервировано для отладочной информации

**Ответ 200:**

```json
{
  "routeCompressed": "<base64(zlib(JSON))>",
  "lengthMeters": 1168113.85,
  "pointsCount": 2981,
  "removedPointsCount": 35,
  "lengthBeforeMeters": 1170260.79,
  "warnings": ["intersect: max iterations reached, route may still contain intersections"]
}
```

- `routeCompressed` — очищенный маршрут в том же формате
- `lengthMeters` — длина очищенного маршрута в метрах
- `pointsCount` — количество точек после обработки
- `removedPointsCount` — сколько точек удалено
- `lengthBeforeMeters` — длина маршрута до обработки
- `warnings` (опциональное) — предупреждения о неполной обработке (например, исчерпание итераций)

**Ошибки:**

| Код | HTTP | Когда |
|---|---|---|
| `INVALID_REQUEST` | 400 | Невалидный JSON или пустой `routeCompressed` |
| `INVALID_ROUTE_FORMAT` | 400 | Не удалось декодировать маршрут (битый base64/zlib/JSON) |
| `ROUTE_TOO_LARGE` | 413 | Точек больше чем `MAX_POINTS` |
| `UNPROCESSABLE_ROUTE` | 422 | После обработки осталось < 2 точек |
| `INTERNAL` | 500 | Внутренняя ошибка |

Формат ошибки:

```json
{
  "error": {
    "code": "INVALID_REQUEST",
    "message": "routeCompressed is required"
  }
}
```

### GET /healthz

Liveness probe. Возвращает `200 ok` пока процесс жив.

### GET /readyz

Readiness probe. MVP: аналогичен `/healthz`. Пост-MVP: будет проверять доступность OSRM.

## Формат routeCompressed

Совместим с PHP-backend:

```
Кодирование: json_encode(points) -> gzcompress(level=9) -> base64_encode
Декодирование: base64_decode -> gzuncompress -> json_decode
```

PHP `gzcompress()` — это **zlib** (заголовок `0x78 0xDA`), не gzip. В Go используется `compress/zlib`.

Формат точки:

```json
{ "t": "2026-03-16T10:12:20+03:00", "pos": { "x": 37.617, "y": 55.755 } }
```

- `t` — время (ISO 8601)
- `pos.x` — долгота (longitude)
- `pos.y` — широта (latitude)
- Минимальный маршрут — 2 точки
- Точки могут приходить неотсортированными — внутри сервиса сортируются по `t`

## Pipeline

Маршрут проходит через цепочку этапов (Stage):

```
routeCompressed
  -> Decode (base64 -> zlib -> JSON -> []Point)
  -> SortByTime
  -> FilterBySpeed         удаляет GPS-телепорты (скорость > MAX_SPEED_KMH)
  -> Deduplicate           склеивает точки ближе DEDUP_DISTANCE_METERS и DEDUP_TIME_GAP
  -> RemoveSelfIntersections   убирает петли от GPS-глюков (с эвристиками)
  -> Encode ([]Point -> JSON -> zlib -> base64)
  -> TotalLength -> lengthMeters
```

Каждый этап — реализация интерфейса `pipeline.Stage`. Добавление нового этапа (OSRM, Simplify) = новая структура в цепочке, без правок в остальном коде.

### Эвристики пересечений

`RemoveSelfIntersections` не удаляет петлю если она превышает `MAX_LOOP_METERS` или `MAX_LOOP_SECONDS` — такие петли считаются реальными (развязки, серпантины, развороты). GPS-глюки почти всегда короткие по времени и маленькие по площади.

## Архитектура

```
cmd/server/              точка входа, graceful shutdown
internal/
  config/                парсинг env-переменных
  codec/                 base64 <-> zlib <-> JSON <-> []geo.Point
  geo/                   Point, Haversine, длина маршрута, пересечение отрезков
  pipeline/              интерфейс Stage + этапы (sort, speed, dedup, intersections)
  api/                   HTTP: router (chi), handler, middleware, errors, health
  osrm/                  заготовка под map matching (пост-MVP)
```

### Middleware (порядок снаружи внутрь)

1. **Recover** — ловит паники, возвращает 500
2. **RequestID** — генерирует UUID v4, ставит заголовок `X-Request-ID`
3. **Logger** — логирует метод, путь, статус, время выполнения
4. **LimitBody** — ограничивает размер тела запроса (`MAX_BODY_BYTES`)
5. **ErrorMiddleware** — превращает `error` из handler в JSON-ответ с правильным статусом

## Конфигурация (env)

| Переменная | Дефолт | Назначение |
|---|---|---|
| `PORT` | `8080` | HTTP порт |
| `READ_TIMEOUT` | `10s` | таймаут чтения запроса |
| `WRITE_TIMEOUT` | `30s` | таймаут записи ответа |
| `SHUTDOWN_TIMEOUT` | `15s` | таймаут graceful shutdown |
| `MAX_BODY_BYTES` | `10485760` (10 МБ) | лимит размера body |
| `DEDUP_DISTANCE_METERS` | `2.0` | порог близости точек для дедупликации |
| `DEDUP_TIME_GAP` | `60s` | максимальный временной разрыв для дедупликации |
| `MAX_POINTS` | `50000` | лимит точек в маршруте |
| `INTERSECT_MAX_ITER` | `10000` | лимит итераций поиска пересечений |
| `MAX_SPEED_KMH` | `150` | порог скорости для фильтра телепортов |
| `MAX_LOOP_METERS` | `100` | петли больше — считаем реальными |
| `MAX_LOOP_SECONDS` | `10` | петли длиннее — считаем реальными |
| `LOG_LEVEL` | `info` | уровень логирования (debug/info/warn/error) |
| `USE_OSRM` | `false` | (пост-MVP) включить OSRM map matching |
| `OSRM_URL` | — | (пост-MVP) URL OSRM-сервиса |

## Зависимости

- [go-chi/chi](https://github.com/go-chi/chi) — HTTP роутер с middleware
- [google/uuid](https://github.com/google/uuid) — генерация UUID v4

Остальное — стандартная библиотека Go.

## Пост-MVP

- OSRM map matching — snap на дороги через внешний OSRM-сервис (заменит `RemoveSelfIntersections`)
- Simplify — упрощение маршрута алгоритмом Дугласа-Пекера
- FilterByAcceleration — фильтр по ускорению (ловит GPS-глюки с резким набором скорости, которые проходят через speed-фильтр)
- `context.Context` в pipeline — для таймаутов OSRM-запросов
