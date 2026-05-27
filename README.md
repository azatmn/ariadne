# ariadne

Stateless Go-микросервис для устранения коллизий GPS-маршрутов: принимает сжатый маршрут от backend, чистит дубликаты/пересечения/петли, возвращает исправленный маршрут и его длину в метрах.

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
- `returnDebug` (опциональное) — если `true`, в ответе появится поле `debug` со статистикой по каждому этапу pipeline

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
- `debug` (опциональное) — статистика по каждому этапу pipeline (появляется при `returnDebug: true`)

**Ошибки:**

| Код | HTTP | Когда |
|---|---|---|
| `INVALID_REQUEST` | 400 | Невалидный JSON, пустой `routeCompressed`, или тело запроса > `MAX_BODY_BYTES` |
| `INVALID_ROUTE_FORMAT` | 400 | Не удалось декодировать маршрут (битый base64/zlib/JSON) |
| `ROUTE_TOO_LARGE` | 413 | Точек больше `MAX_POINTS` или распакованные данные > `MAX_DECOMPRESSED_BYTES` |
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

### GET /swagger/*

Swagger UI — интерактивная документация API. Доступна по адресу `/swagger/index.html`. Отключается через `SWAGGER_ENABLED=false`.

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
  -> RemoveSelfIntersections   убирает петли от GPS-глюков (с эвристиками, поддерживает context cancellation)
  -> Encode ([]Point -> JSON -> zlib -> base64)
  -> TotalLength -> lengthMeters
```

Каждый этап — реализация интерфейса `pipeline.Stage`. Добавление нового этапа (OSRM, Simplify) = новая структура в цепочке, без правок в остальном коде.

### Эвристики пересечений

`RemoveSelfIntersections` не удаляет петлю если она превышает `MAX_LOOP_METERS` или `MAX_LOOP_SECONDS` — такие петли считаются реальными (развязки, серпантины, развороты). GPS-глюки почти всегда короткие по времени и маленькие по площади.

## gRPC API

Второй транспорт рядом с REST. Оба вызывают `service.Resolve`.

### ariadne.v1.RouteService/ResolveCollisions

```text
message ResolveCollisionsRequest {
  string route_compressed = 1;
  bool return_debug = 2;
}

message ResolveCollisionsResponse {
  string route_compressed = 1;
  double length_meters = 2;
  double length_before_meters = 3;
  int32 points_count = 4;
  int32 removed_points_count = 5;
  repeated string warnings = 6;
  repeated StageStats debug = 7;
}
```

**Коды ошибок:**

| gRPC код | Когда |
|---|---|
| `INVALID_ARGUMENT` | Пустой `route_compressed`, невалидный base64/zlib/JSON |
| `RESOURCE_EXHAUSTED` | Точек > `MAX_POINTS`, распакованные данные > `MAX_DECOMPRESSED_BYTES`, сообщение > `GRPC_MAX_RECV_MSG_SIZE` |
| `DEADLINE_EXCEEDED` | Таймаут обработки |
| `INTERNAL` | Внутренняя ошибка |

**Metadata:** заголовок `x-request-id` возвращается в response metadata (и при успехе, и при ошибке).

### grpc.health.v1.Health/Check

Стандартный gRPC health check. Поддерживает проверку конкретного сервиса:

```json
{"service": "ariadne.v1.RouteService"}
```

Или общий статус (пустой `service`).

### Server reflection

Управляется переменной `GRPC_REFLECTION` (по умолчанию `false`). При `GRPC_REFLECTION=true` — Postman и `grpcurl` автоматически обнаруживают все методы без загрузки proto-файла.

## Архитектура

```
cmd/server/              точка входа, два сервера (HTTP + gRPC), graceful shutdown
internal/
  config/                парсинг env-переменных в Config
  codec/                 base64 <-> zlib <-> JSON <-> []geo.Point (с защитой от zip bomb)
  geo/                   Point, Haversine, длина маршрута, пересечение отрезков
  gen/                   сгенерированный Go-код из proto (не редактировать)
  logger/                scoped логгер в context (request_id в каждой записи)
  service/               бизнес-логика (Resolve: валидация → pipeline → длина)
  pipeline/              интерфейс Stage + этапы (sort, speed, dedup, intersections)
  api/                   HTTP: router (chi), handler, middleware, errors, health
  grpcapi/               gRPC: handler, interceptors, server, health
  osrm/                  заготовка под map matching (пост-MVP)
proto/                   Proto-файлы (.proto)
swagger/                 сгенерированная Swagger-спека (swag init)
```

### REST Middleware (порядок снаружи внутрь)

1. **Recover** — ловит паники, возвращает 500
2. **RequestID** — генерирует UUID v4, ставит заголовок `X-Request-ID`, создаёт scoped логгер с `request_id` в context
3. **Logger** — логирует метод, путь, статус, время выполнения
4. **LimitBody** — ограничивает размер тела запроса (`MAX_BODY_BYTES`)
5. **ErrorMiddleware** — превращает `error` из handler в JSON-ответ с правильным статусом

### gRPC Interceptors (порядок снаружи внутрь)

1. **RecoverInterceptor** — ловит паники, возвращает `codes.Internal`
2. **RequestIDInterceptor** — генерирует UUID v4, кладёт scoped логгер в context, отправляет `x-request-id` в response metadata
3. **LoggerInterceptor** — логирует метод, gRPC-код, время выполнения

## Конфигурация (env)

| Переменная | Дефолт | Назначение |
|---|---|---|
| **Server** | | |
| `PORT` | `8080` | HTTP порт |
| `GRPC_PORT` | `9090` | gRPC порт |
| `GRPC_MAX_RECV_MSG_SIZE` | `10485760` (10 МБ) | лимит размера входящего gRPC-сообщения |
| `READ_TIMEOUT` | `10s` | таймаут чтения HTTP-запроса |
| `WRITE_TIMEOUT` | `30s` | таймаут записи HTTP-ответа |
| `IDLE_TIMEOUT` | `2m` | таймаут keep-alive соединений без активности |
| `SHUTDOWN_TIMEOUT` | `15s` | таймаут graceful shutdown |
| `RESOLVE_TIMEOUT` | `25s` | таймаут обработки маршрута |
| **Limits** | | |
| `MAX_BODY_BYTES` | `10485760` (10 МБ) | лимит размера HTTP body |
| `MAX_DECOMPRESSED_BYTES` | `20971520` (20 МБ) | лимит после распаковки zlib (защита от zip bomb) |
| `MAX_POINTS` | `50000` | максимум точек в маршруте |
| **Pipeline** | | |
| `DEDUP_DISTANCE_METERS` | `2.0` | порог близости точек для дедупликации (метры) |
| `DEDUP_TIME_GAP` | `60s` | максимальный временной разрыв для дедупликации |
| `MAX_SPEED_KMH` | `150` | скорость выше — GPS-телепорт, точка удаляется |
| `MAX_LOOP_METERS` | `100` | петли больше — считаем реальными (не удаляем) |
| `MAX_LOOP_SECONDS` | `10` | петли длиннее — считаем реальными (не удаляем) |
| `INTERSECT_MAX_ITER` | `10000` | лимит итераций поиска пересечений |
| **Swagger** | | |
| `SWAGGER_ENABLED` | `false` | `true` — включает `/swagger/*` эндпоинт |
| **Logging** | | |
| `GRPC_REFLECTION` | `false` | `true` — включает gRPC server reflection |
| `LOG_LEVEL` | `info` | уровень логирования (debug/info/warn/error) |
| **OSRM (пост-MVP)** | | |
| `USE_OSRM` | `false` | включить OSRM map matching |
| `OSRM_URL` | — | URL OSRM-сервиса |

## Зависимости

- [go-chi/chi](https://github.com/go-chi/chi) — HTTP роутер с middleware
- [google/uuid](https://github.com/google/uuid) — генерация UUID v4
- [google.golang.org/grpc](https://pkg.go.dev/google.golang.org/grpc) — gRPC сервер, interceptors, health check
- [google.golang.org/protobuf](https://pkg.go.dev/google.golang.org/protobuf) — protobuf runtime
- [swaggo/swag](https://github.com/swaggo/swag) — генерация OpenAPI-спеки из аннотаций
- [swaggo/http-swagger](https://github.com/swaggo/http-swagger) — Swagger UI middleware для chi
- [stretchr/testify](https://github.com/stretchr/testify) — assert/require для тестов

Остальное — стандартная библиотека Go.

## Защита от перегрузки

Три уровня ограничения размера данных (каждый следующий ловит то, что пропустил предыдущий):

**REST:**
1. **`MAX_BODY_BYTES`** (10 МБ) — middleware `LimitBody` через `http.MaxBytesReader`. Отсекает слишком большие HTTP-запросы до чтения в память.
2. **`MAX_DECOMPRESSED_BYTES`** (20 МБ) — `io.LimitReader` вокруг zlib-распаковки в `codec.Decode`. Защита от zip bomb.
3. **`MAX_POINTS`** (50 000) — бизнес-лимит в `service.Resolve`. Ограничивает количество точек маршрута.

**gRPC:**
1. **`GRPC_MAX_RECV_MSG_SIZE`** (10 МБ) — `grpc.MaxRecvMsgSize`. Отсекает слишком большие protobuf-сообщения.
2. **`MAX_DECOMPRESSED_BYTES`** (20 МБ) — аналогично REST.
3. **`MAX_POINTS`** (50 000) — аналогично REST.

## Запуск

```bash
cp .env.example .env   # скопировать шаблон, при необходимости поправить значения
make run               # go run ./cmd/server
```

Или через Docker Compose:

```bash
docker compose up --build
```

`docker-compose.yml` подтягивает переменные из `.env` через `env_file`. Пробрасывает порты `8080` (HTTP) и `9090` (gRPC).

### Генерация proto

```bash
make proto
```

Требует установленных `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc`.

### Генерация Swagger

```bash
make swagger
```

Требует установленного `swag` (`go install github.com/swaggo/swag/cmd/swag@latest`). Генерирует `swagger/docs.go` и `swagger/swagger.json` из аннотаций в коде.

## CI/CD (GitLab)

Pipeline запускается автоматически при пуше в GitLab:

```
git push → .gitlab-ci.yml →
  1. test:   go vet + go test -race (на каждый пуш и MR)
  2. build:  docker build → push в GitLab Registry (только main)
  3. deploy: webhook (TODO — ждём URL и токен от команды)
```

- Тесты бегут в кастомном образе `$CI_REGISTRY_IMAGE/ci:1.26` (Go + gcc для race detector)
- Go-модули кешируются между запусками (`/go/pkg/mod/`)
- Docker-образ тегается `$CI_COMMIT_SHORT_SHA` + `latest`

## Пост-MVP

- OSRM map matching — snap на дороги через внешний OSRM-сервис (заменит `RemoveSelfIntersections`)
- Simplify — упрощение маршрута алгоритмом Дугласа-Пекера
- FilterByAcceleration — фильтр по ускорению (ловит GPS-глюки с резким набором скорости, которые проходят через speed-фильтр)
- Auth middleware — Bearer token из env

## Автор

**Azat Minyazov** — [Telegram](https://t.me/azatmn) — minyazovazat@gmail.com
