# ariadne

Go-микросервис для очистки GPS-маршрутов (устранение «коллизий»/глюков). Работает **асинхронно**: клиент сдаёт маршрут → получает `taskKey` → сервис чистит его в фоне → по готовности дёргает callback и/или клиент забирает результат по `taskKey`.

Хранилище задач и результатов — **Redis** (логическая БД 10, TTL 1 час). Обработку ведёт пул воркеров (число из env). Два транспорта: **REST** и **gRPC**.

## Асинхронный поток

```
1. Клиент → POST /v1/tasks {routeCompressed}          → 202 {taskKey}
2. Сервис: карточка задачи (pending) в Redis + в очередь
3. Воркер: берёт из очереди → чистит → пишет результат (done) или ошибку (failed) в Redis
4. Воркер → POST на CALLBACK_URL с taskKey            (готово; если CALLBACK_URL задан)
5. Клиент → GET /v1/tasks/{taskKey}                    → статус + очищенный маршрут + длина
             GET /v1/tasks/{taskKey}/debug             → разбор по стадиям
```

Все ключи задачи живут в Redis 1 час (`RESULT_TTL`), потом авто-удаление.

## REST API

### POST /v1/tasks — сдать задачу

Мгновенно принимает маршрут в очередь. **Вход не декодится** (это работа воркера) — submit быстрый.

**Запрос:**
```json
{ "routeCompressed": "<base64(zlib(JSON))>" }
```

**Ответ 202:**
```json
{ "taskKey": "a1b2c3d4-..." }
```

`taskKey` — UUID, уникальность гарантируется записью в Redis через `SET NX`.

### GET /v1/tasks/{taskKey} — статус и результат

**Ответ 200:**
```json
{
  "taskKey": "a1b2c3d4-...",
  "status": "done",
  "routeCompressed": "<base64(zlib(JSON))>",
  "lengthMeters": 1168113.85
}
```

- `status` — `pending` (в очереди/считается) / `done` (готово) / `failed` (упало)
- `routeCompressed`, `lengthMeters` — только при `status: done` (очищенный маршрут в том же сжатом формате + его длина в метрах)
- `error` — только при `status: failed` (текст, почему упало: битый вход, слишком много точек, < 2 точек после чистки и т.п.)
- `404 NOT_FOUND` — задачи с таким `taskKey` нет (не создавали или протухла по TTL)

### GET /v1/tasks/{taskKey}/debug — разбор по стадиям

**Ответ 200:**
```json
{
  "taskKey": "a1b2c3d4-...",
  "status": "done",
  "debug": [
    { "name": "sort_by_time", "pointsBefore": 3016, "pointsAfter": 3016, "elapsed": "1.2ms" },
    { "name": "collapse_stops", "pointsBefore": 2329, "pointsAfter": 1946, "elapsed": "3.4ms" }
  ]
}
```

`debug` заполнен при `status: done` (статистика по каждой стадии pipeline). `404` — задачи нет.

### Прочее

- `GET /healthz` — liveness, `200 ok`.
- `GET /readyz` — readiness, `503` при shutdown.
- `GET /swagger/*` — Swagger UI (если `SWAGGER_ENABLED=true`).

### Ошибки REST

| Код | HTTP | Когда |
|---|---|---|
| `INVALID_REQUEST` | 400 | Невалидный JSON, пустой `routeCompressed`, тело > `MAX_BODY_BYTES` |
| `NOT_FOUND` | 404 | Нет задачи с таким `taskKey` |
| `INTERNAL` | 500 | Внутренняя ошибка (например, Redis недоступен) |

Формат ошибки:
```json
{ "error": { "code": "INVALID_REQUEST", "message": "routeCompressed is required" } }
```

`requestId` — в заголовке `X-Request-ID` (не в теле). Ошибки самой обработки маршрута (битый вход, слишком много/мало точек) НЕ приходят как HTTP-ошибка submit'а — они всплывают в `status: failed` + `error` при опросе.

## gRPC API — `ariadne.v1.RouteService`

```proto
service RouteService {
  rpc SubmitTask   (SubmitTaskRequest)    returns (SubmitTaskResponse);
  rpc GetTask      (GetTaskRequest)        returns (GetTaskResponse);
  rpc GetTaskDebug (GetTaskDebugRequest)   returns (GetTaskDebugResponse);
}

message SubmitTaskRequest  { string route_compressed = 1; }
message SubmitTaskResponse { string task_key = 1; }

message GetTaskRequest  { string task_key = 1; }
message GetTaskResponse {
  string task_key = 1;
  string status = 2;             // pending / done / failed
  string route_compressed = 3;   // при done
  double length_meters = 4;      // при done
  string error = 5;              // при failed
}

message GetTaskDebugRequest  { string task_key = 1; }
message GetTaskDebugResponse {
  string task_key = 1;
  string status = 2;
  repeated StageStats debug = 3;
}
```

**Коды ошибок gRPC:**

| Код | Когда |
|---|---|
| `INVALID_ARGUMENT` | Пустой `route_compressed` или пустой `task_key` |
| `NOT_FOUND` | Нет задачи с таким `task_key` |
| `RESOURCE_EXHAUSTED` | Сообщение > `GRPC_MAX_RECV_MSG_SIZE` |
| `INTERNAL` | Внутренняя ошибка (Redis и т.п.) |

`x-request-id` возвращается в response-метаданных (и при успехе, и при ошибке). Есть стандартный `grpc.health.v1.Health/Check`; server reflection — по `GRPC_REFLECTION`.

## Callback (Go → Laravel)

Когда воркер закончил задачу (`done` ИЛИ `failed`), он дёргает внешнюю систему:

- **Метод:** POST на `CALLBACK_URL` с подстановкой плейсхолдера `{taskKey}` (напр. `https://laravel/api/ariadne/{taskKey}`).
- **Тело:** `{ "taskKey": "...", "status": "done|failed" }` (основной сигнал — `taskKey` в URL; по нему клиент идёт в `GET /v1/tasks/{taskKey}`).
- **Ретраи:** на сетевую ошибку и 5xx — до `CALLBACK_RETRIES` раз с бэкоффом; на 4xx не повторяет.
- **Выключение:** пустой `CALLBACK_URL` → коллбэки не шлются.

> Тело/метод — разумные дефолты; при уточнении контракта от команды меняются в `internal/callback`.

## Формат routeCompressed

Совместим с PHP-backend:
```
Кодирование:  json_encode(points) → gzcompress(level=9) → base64_encode
Декодирование: base64_decode → gzuncompress → json_decode
```
PHP `gzcompress()` — это **zlib** (заголовок `0x78 0xDA`), не gzip. В Go — `compress/zlib`.

Формат точки:
```json
{ "t": "2026-03-16T10:12:20+03:00", "pos": { "x": 37.617, "y": 55.755 } }
```
- `t` — время (ISO 8601); `pos.x` — долгота; `pos.y` — широта.
- Минимум 2 точки; приходить могут неотсортированными (сортируются внутри по `t`).

## Pipeline очистки

Маршрут проходит цепочку независимых стадий (`pipeline.Stage`):

```
Decode → sort_by_time
       → remove_anchor_backtrack   режет «откаты» относительно якорей (первая/последняя точка); 0 = выкл
       → remove_teleports          вырезает спуфинг-загоны (телепорт в аэропорт + возврат)
       → filter_by_speed           убирает точки со скоростью > MAX_SPEED_KMH
       → filter_by_acceleration    убирает точки с ускорением > MAX_ACCEL_KMH_PER_SEC
       → deduplicate               склеивает дубли (ближе DEDUP_DISTANCE_METERS И Δt < DEDUP_TIME_GAP)
       → collapse_stops            сворачивает стоянки (кучи точек в радиусе) в одну
       → simplify                  Дугласа-Пекер (SIMPLIFY_MIN_METERS)
       → Encode → длина (TotalLength)
```

Дедлайн обработки (`RESOLVE_TIMEOUT`) проверяется между стадиями. Добавление стадии = новая структура в цепочке `pipeline.New`, без правок в остальном коде.

## Архитектура

```
cmd/
  server/        прод: HTTP + gRPC (async) + пул воркеров + подключение к Redis; graceful shutdown
  debugserver/   ТОЛЬКО для отладки: синхронная /v1/routes/resolve-collisions (без Redis/воркеров)
internal/
  config/        env → Config
  codec/         base64 ↔ zlib ↔ JSON ↔ []geo.Point (защита от zip bomb)
  geo/           Point, Haversine, длина, расстояние до отрезка
  taskstore/     Redis-слой: карточка задачи (task:{taskKey}) + очередь (tasks:queue), TTL
  worker/        пул воркеров: очередь → чистка → результат в Redis → callback
  callback/      HTTP-клиент уведомления внешней системы (Laravel)
  service/       бизнес-логика Resolve: валидация → pipeline → длина
  pipeline/      Stage + стадии очистки
  api/           REST (async): router (chi), handler (submit/status/debug), middleware, errors, health
  grpcapi/       gRPC (async): handler, interceptors, server, health
  debugapi/      синхронный HandleResolve — только для debugserver (не для прода)
  gen/           сгенерированный код из proto (не редактировать)
  logger/        scoped-логгер в context (request_id в каждой записи)
proto/           .proto
swagger/         сгенерированная Swagger-спека
```

Прод-`internal/api` — чисто асинхронный. Синхронная ручка вынесена в `internal/debugapi` + `cmd/debugserver`, чтобы прод её не содержал (на ней держится фронт-отладчик `ariadne-debug-proxy`).

### Middleware / Interceptors

- **REST:** Recover → RequestID (`X-Request-ID`) → Logger → LimitBody → ErrorMiddleware.
- **gRPC:** RecoverInterceptor → RequestIDInterceptor (`x-request-id` в metadata) → LoggerInterceptor.

## Конфигурация (env)

| Переменная | Дефолт | Назначение |
|---|---|---|
| **Server** | | |
| `PORT` | `8080` | HTTP порт |
| `GRPC_PORT` | `9090` | gRPC порт |
| `GRPC_MAX_RECV_MSG_SIZE` | `10485760` | лимит входящего gRPC-сообщения |
| `READ_TIMEOUT` / `WRITE_TIMEOUT` / `IDLE_TIMEOUT` | `10s` / `30s` / `2m` | HTTP-таймауты |
| `SHUTDOWN_TIMEOUT` | `15s` | таймаут graceful shutdown |
| `RESOLVE_TIMEOUT` | `25s` | таймаут обработки одной задачи воркером |
| **Limits** | | |
| `MAX_BODY_BYTES` | `10485760` | лимит HTTP body |
| `MAX_DECOMPRESSED_BYTES` | `20971520` | лимит после распаковки zlib (zip bomb) |
| `MAX_POINTS` | `50000` | максимум точек в маршруте |
| **Pipeline** | | |
| `ANCHOR_BACKTRACK_TOLERANCE_METERS` | `0` | порог отката для якорного фильтра; `0` = выкл |
| `TELEPORT_JUMP_METERS` | `15000` | скачок больше = подозрение на телепорт |
| `TELEPORT_RETURN_METERS` | `2000` | возврат ближе = телепорт-загон |
| `TELEPORT_MAX_SPAN_METERS` | `5000` | вырезаем загон только если его размах меньше |
| `MAX_SPEED_KMH` | `150` | выше — GPS-телепорт |
| `MAX_ACCEL_KMH_PER_SEC` | `20` | выше — GPS-глюк по ускорению |
| `DEDUP_DISTANCE_METERS` | `2.0` | порог близости для дедупа |
| `DEDUP_TIME_GAP` | `60s` | окно времени для дедупа |
| `STOP_RADIUS_METERS` | `50` | размер пятна стоянки |
| `STOP_MIN_POINTS` | `5` | от скольких точек в пятне = стоянка |
| `SIMPLIFY_MIN_METERS` | `5.0` | Дугласа-Пекер |
| **Redis** | | |
| `REDIS_ADDR` | `localhost:6379` | адрес Redis |
| `REDIS_DB` | `10` | логическая база |
| `REDIS_PASSWORD` | — | пароль (пусто = без) |
| `WORKER_COUNT` | `4` | число воркеров-горутин |
| `RESULT_TTL` | `1h` | сколько задача живёт в Redis |
| **Callback** | | |
| `CALLBACK_URL` | — | шаблон с `{taskKey}`; пусто = коллбэки выкл |
| `CALLBACK_RETRIES` | `3` | повторов сверх первой попытки |
| `CALLBACK_TIMEOUT` | `5s` | таймаут одного запроса коллбэка |
| **Прочее** | | |
| `SWAGGER_ENABLED` | `false` | `/swagger/*` |
| `GRPC_REFLECTION` | `false` | gRPC reflection |
| `LOG_LEVEL` | `info` | debug/info/warn/error |

## Защита от перегрузки (3 уровня)

- **REST:** `MAX_BODY_BYTES` (middleware) → `MAX_DECOMPRESSED_BYTES` (codec, zip bomb) → `MAX_POINTS` (service).
- **gRPC:** `GRPC_MAX_RECV_MSG_SIZE` → `MAX_DECOMPRESSED_BYTES` → `MAX_POINTS`.

## Запуск

Прод (нужен Redis):
```bash
cp .env.example .env
docker compose up --build          # ariadne + redis
# или локально: make run  (go run ./cmd/server) — Redis подними отдельно
```
`docker-compose.yml` тянет переменные из `.env` (`env_file`), пробрасывает `8080`/`9090`, поднимает `redis`.

Отладочный синхронный сервер (для фронт-отладчика, без Redis/воркеров):
```bash
go run ./cmd/debugserver           # слушает :8080, ручка /v1/routes/resolve-collisions
```

Генерация: `make proto` (нужны `protoc` + плагины), `make swagger` (нужен `swag`).

## CI/CD (GitLab)

```
git push → .gitlab-ci.yml → test (go vet + go test -race) → build (docker → Registry, только main) → deploy (webhook, TODO)
```

## Автор

**Azat Minyazov** — [Telegram](https://t.me/azatmn)
