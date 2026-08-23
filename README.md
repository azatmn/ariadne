# ariadne

Go-микросервис для очистки GPS-маршрутов. Работает асинхронно: клиент сдаёт маршрут → получает `taskKey` → сервис чистит его в фоне → по готовности дёргает callback и/или клиент забирает результат по `taskKey`.

Хранилище задач и результатов — Redis (логическая БД 10, TTL 1 час). Обработку ведёт пул воркеров. Два транспорта: REST и gRPC.

## Асинхронный поток

```
1. Клиент → POST /v1/tasks {routeCompressed}          → 202 {taskKey}
2. Сервис: карточка задачи (pending) в Redis + в очередь
3. Воркер: берёт из очереди → чистит → пишет результат (done) или ошибку (failed)
4. Воркер → POST на CALLBACK_URL с taskKey            (если CALLBACK_URL задан)
5. Клиент → GET /v1/tasks/{taskKey}                    → статус + маршрут + длина
             GET /v1/tasks/{taskKey}/debug             → разбор по стадиям
```

Все ключи задачи живут `RESULT_TTL`, потом удаляются автоматически.

### Очередь

Очередь — поток Redis (stream) с группой потребителей. Задача числится за тем, кто её взял, пока он не распишется:

```
взял (XREADGROUP) → посчитал → СОХРАНИЛ результат → расписался (XACK)
                                      ↑
                        не сохранилось — расписки нет,
                        задачу вернут в работу
```

- **Уборщик** (раз в минуту) возвращает в очередь задачи, выданные и не подтверждённые дольше порога. Порог — не отдельная настройка, а `2 × (RESOLVE_TIMEOUT + таймауты Redis)`; вычисленное значение печатается в лог на старте.
- **Предел попыток — 5.** Номер попытки хранится в самой записи. Задача, оборвавшая обработку пять раз, помечается `failed` с причиной.
- **Потеря данных Redis лечится сама.** Если группа потребителей исчезла, воркер поднимает её и повторяет попытку — но только если очередь уже готовили в этом процессе (забытый `EnsureQueue` на старте по-прежнему падает громко) и только по факту ошибки `NOGROUP`.
- Восстановленная группа читает ленту с начала, поэтому воркер **не считает заново** задачу, чья карточка уже `done` или `failed`, — только расписывается.
- Между неудачными попытками взять задачу воркер ждёт 1 с.
- Молчание очереди дольше минуты видно снаружи: `/readyz` отвечает `503`.

## REST API

### POST /v1/tasks — сдать задачу

Вход не декодится (это работа воркера), поэтому submit быстрый.

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
  "lengthMeters": 1168113.85,
  "degraded": true,
  "warnings": ["fill_gaps: budget spent, 35 of 294 gaps left straight — mileage understated"]
}
```

| поле | когда | что значит |
|---|---|---|
| `status` | всегда | `pending` / `done` / `failed` |
| `routeCompressed` | при `done` | очищенный маршрут в том же сжатом формате |
| `lengthMeters` | всегда | длина в метрах; при `pending`/`failed` равна `0` (поле без `omitempty`, чтобы отличать `0` от отсутствия) |
| `error` | при `failed` | почему упало: битый вход, слишком много точек, меньше 2 точек после чистки |
| `degraded` | при `done` | километражу верить с оговоркой: маршрут настоящий, но неполный — не хватило времени или не задан маршрутизатор. Отдельным полем, а не значением `status`, чтобы не сломать клиента, который сверяется с `done` |
| `warnings` | при `done` | та же оговорка словами, для разбора человеком |

`404 NOT_FOUND` — задачи с таким `taskKey` нет (не создавали или протухла по TTL).

> В ответе есть точки, которых не было во входе: дорисовка прокладывает их по дорожной сети там, где пропадала связь, а время на них раскладывает по доле пути. Отдельным полем они не помечаются — конвейер помечает их только внутри себя (`RunState.Synthetic`).

### GET /v1/tasks/{taskKey}/debug — разбор по стадиям

**Ответ 200:**
```json
{
  "taskKey": "a1b2c3d4-...",
  "status": "done",
  "debug": [
    { "name": "sort_by_time", "pointsBefore": 3016, "pointsAfter": 3016, "elapsed": "1.2ms" },
    {
      "name": "core",
      "pointsBefore": 3016, "pointsAfter": 1946, "elapsed": "2.1s",
      "extra": {
        "roadPasses": 4, "roadBanned": 129, "roadAsked": 1730,
        "stopsTotal": 7, "stopsTrusted": 1, "stopsFrozen": 1,
        "split": 0, "spread": 1155, "amnesty": 106, "loops": 0,
        "snapMedianM": 5.2, "snapFraction": 1, "degraded": false,
        "kmBefore": 3000.4, "kmAfter": 288.1
      }
    },
    {
      "name": "fill_gaps",
      "pointsBefore": 667, "pointsAfter": 1114, "elapsed": "0.9s",
      "extra": {
        "gaps": 29, "filled": 27, "addedM": 32104.5, "addedPts": 447,
        "reasons": { "accepted": 27, "detour": 1, "physics": 1 },
        "degraded": false
      }
    }
  ]
}
```

`debug` заполнен при `status: done`. `extra` есть у стадий, которым есть что рассказать. У упавшей стадии заполняется `error`, статистика по уже пройденным не теряется. `404` — задачи нет.

### Прочее

- `GET /healthz` — liveness, `200 ok`, пока жив процесс (в том числе когда лежит Redis).
- `GET /readyz` — readiness: `503 shutting down` при остановке, `503 queue is not being processed` при мёртвой очереди.
- `GET /swagger/*` — Swagger UI (если `SWAGGER_ENABLED=true`).

### Ошибки REST

| Код | HTTP | Когда |
|---|---|---|
| `INVALID_REQUEST` | 400 | невалидный JSON, пустой `routeCompressed`, тело > `MAX_BODY_BYTES` |
| `NOT_FOUND` | 404 | нет задачи с таким `taskKey` |
| `INTERNAL` | 500 | внутренняя ошибка (например, Redis недоступен) |

```json
{ "error": { "code": "INVALID_REQUEST", "message": "routeCompressed is required" } }
```

`requestId` — в заголовке `X-Request-ID`, не в теле. Ошибки обработки маршрута приходят не как HTTP-ошибка submit'а, а как `status: failed` + `error` при опросе.

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
  bool degraded = 6;             // при done
  repeated string warnings = 7;  // при done
}

message GetTaskDebugRequest  { string task_key = 1; }
message GetTaskDebugResponse {
  string task_key = 1;
  string status = 2;
  repeated StageStats debug = 3;
}
```

| Код | Когда |
|---|---|
| `INVALID_ARGUMENT` | пустой `route_compressed` или `task_key` |
| `NOT_FOUND` | нет задачи с таким `task_key` |
| `RESOURCE_EXHAUSTED` | сообщение > `GRPC_MAX_RECV_MSG_SIZE` |
| `INTERNAL` | внутренняя ошибка |

`x-request-id` возвращается в response-метаданных. Есть `grpc.health.v1.Health/Check`; server reflection — по `GRPC_REFLECTION`.

## Callback (Go → Laravel)

Когда воркер закончил задачу (`done` или `failed`), он дёргает внешнюю систему:

- **Метод:** POST на `CALLBACK_URL` с подстановкой `{taskKey}` (например `https://laravel/api/ariadne/{taskKey}`).
- **Тело:** `{ "taskKey": "...", "status": "done|failed" }`.
- **Повторы:** на сетевую ошибку и 5xx — до `CALLBACK_RETRIES` раз с бэкоффом; на 4xx не повторяет.
- **Выключение:** пустой `CALLBACK_URL`.

## Формат routeCompressed

Совместим с PHP-backend:

```
Кодирование:   json_encode(points) → gzcompress(level=9) → base64_encode
Декодирование: base64_decode → gzuncompress → json_decode
```

PHP `gzcompress()` — это **zlib** (заголовок `0x78 0xDA`), не gzip. В Go — `compress/zlib`.

Формат точки:
```json
{ "t": "2026-03-16T10:12:20+03:00", "pos": { "x": 37.617, "y": 55.755 } }
```

`t` — время (ISO 8601), `pos.x` — долгота, `pos.y` — широта. Минимум 2 точки; порядок любой, сортируются внутри.

## Pipeline очистки

```
Decode → sort_by_time          сортировка по времени
       → core                  ЧИСТКА: выбор самой тяжёлой физически связной цепочки точек
       → deduplicate           склеивает дубли (ближе DEDUP_DISTANCE_METERS и Δt < DEDUP_TIME_GAP)
       → collapse_stops        сворачивает стоянки в одну точку
       → simplify              Дугласа-Пекер (SIMPLIFY_MIN_METERS), не трогая осмысленные точки
       → reachability_guard    возвращает точки там, где упаковка сделала переход непроезжаемым
       → fill_gaps             ДОРИСОВКА: дыры связи прокладываются по дорогам
       → Encode → длина (TotalLength)
```

**Порядок стадий менять нельзя.** Дорисовка идёт последней и работает по уже очищенному треку: проложить дорогу через выброшенный спуфинг значит узаконить его. Страж стоит после упаковки, потому что чинит именно её работу.

Добавление стадии = новая структура в цепочке `pipeline.New`, без правок в остальном коде.

### Чистка (`internal/core`)

Из трека выбирается самая тяжёлая физически связная последовательность точек. Вес точки — близость к дорожной сети (расстояние даёт OSRM), поверх ложатся привилегии стоянкам и штрафы правил: ловушки, острова, два источника координат в одном треке, петли, лётные поля, приманки. Правила не удаляют точки, а копят улики; удаляет один раз выбор цепочки.

Переходы выбранной цепочки проверяются по дорогам, невозможные запрещаются, цепочка строится заново — до дюжины проходов.

Прежние четыре фильтра (якорь, телепорты, скорость, ускорение) в состав не входят: локальный фильтр решает по одному соседу, и одна плохая опора убивает всё за собой. Файлы и тесты оставлены в репозитории.

### Дорисовка (`fill_gaps`)

Между двумя честными точками трек идёт прямой, а машина ехала по дороге. Дорисовка спрашивает у OSRM настоящий маршрут и вставляет его геометрию, раскладывая времена по доле пути.

Предохранители стоят на крюке (путь по дорогам / прямая): до ×1.3 принимается сразу, выше ×3 отклоняется всегда, между ними решает время (90 км/ч + допуск).

### Отказ и деградация

| случай | поведение |
|---|---|
| OSRM ответил про < 90 % точек | **отказ** (`failed`): ошибка кратная и незаметная — 50 % ответов дают 50 % километража |
| кончился бюджет времени | результат + `degraded`: ошибка ограничена, у дорисовки недоспрошенные дыры остаются прямыми |
| `OSRM_URL` пустой | трек проходит насквозь, `degraded` + предупреждение |
| воркер умер, Redis моргнул | задача возвращается в очередь уборщиком |
| задача обрывает обработку 5 раз подряд | `failed` с причиной |

Бюджет задачи (`RESOLVE_TIMEOUT`) делится: чистка получает 80 % оставшегося времени, хвост достаётся дорисовке.

Бюджет зависит от скорости маршрутизатора и от «грязи» маршрута, а не от числа точек: трек на 24 тысячи точек может считаться 8 секунд, а на 37 тысяч — 44. При смене OSRM его нужно мерить заново.

## Архитектура

```
cmd/
  server/        прод: HTTP + gRPC (async) + пул воркеров + Redis; graceful shutdown
  debugserver/   ТОЛЬКО для отладки: синхронная /v1/routes/resolve-collisions (без Redis/воркеров)
internal/
  config/        env → Config
  codec/         base64 ↔ zlib ↔ JSON ↔ []geo.Point (защита от zip bomb)
  geo/           Point, Haversine, длина, расстояние до отрезка
  taskstore/     Redis: карточка задачи (task:{taskKey}) + очередь-поток (tasks:stream), TTL
  worker/        пул воркеров: очередь → чистка → результат → расписка → callback; уборщик брошенных задач
  callback/      HTTP-клиент уведомления внешней системы
  service/       Resolve: валидация → pipeline → длина; сборка клиента OSRM
  core/          ЧИСТКА: стоянки, веса, правила, выбор цепочки, проверка по дорогам
  osrm/          клиент маршрутизатора: снэпы, расстояния по дорогам, геометрия
  pipeline/      Stage + стадии очистки + общий блокнот прогона (RunState)
  api/           REST (async): router (chi), handler, middleware, errors, health
  grpcapi/       gRPC (async): handler, interceptors, server, health
  debugapi/      синхронный HandleResolve — только для debugserver
  gen/           сгенерированный код из proto (не редактировать)
  logger/        scoped-логгер в context (request_id в запросах, taskKey в обработке)
proto/           .proto
swagger/         сгенерированная Swagger-спека
```

- **Порядок остановки:** сперва перестаём принимать (HTTP и gRPC), потом дорабатываем взятое. Закреплён в `gracefulStop` и тестами.
- **Язык:** тексты ошибок, предупреждений и события в логе — по-английски; комментарии и сообщения тестов — по-русски.
- Прод-`internal/api` асинхронный. Синхронная ручка живёт в `internal/debugapi` + `cmd/debugserver` (на ней держится фронт-отладчик `ariadne-debug-proxy`).

**Middleware:** REST — Recover → RequestID (`X-Request-ID`) → Logger → LimitBody → ErrorMiddleware; gRPC — Recover → RequestID (`x-request-id`) → Logger.

## Конфигурация (env)

| Переменная | Дефолт | Назначение |
|---|---|---|
| **Server** | | |
| `PORT` | `8080` | HTTP порт |
| `GRPC_PORT` | `9090` | gRPC порт |
| `GRPC_MAX_RECV_MSG_SIZE` | `10485760` | лимит входящего gRPC-сообщения |
| `READ_TIMEOUT` / `WRITE_TIMEOUT` / `IDLE_TIMEOUT` | `10s` / `30s` / `2m` | HTTP-таймауты |
| `SHUTDOWN_TIMEOUT` | `15s` | таймаут graceful shutdown |
| `RESOLVE_TIMEOUT` | `60s` | бюджет на одну задачу |
| **Limits** | | |
| `MAX_BODY_BYTES` | `10485760` | лимит HTTP body |
| `MAX_DECOMPRESSED_BYTES` | `20971520` | лимит после распаковки zlib |
| `MAX_POINTS` | `50000` | максимум точек в маршруте |
| **Pipeline** | | |
| `DEDUP_DISTANCE_METERS` | `2.0` | порог близости для дедупа |
| `DEDUP_TIME_GAP` | `60s` | окно времени для дедупа |
| `STOP_RADIUS_METERS` | `50` | размер пятна стоянки |
| `STOP_MIN_POINTS` | `5` | от скольких точек в пятне = стоянка |
| `SIMPLIFY_MIN_METERS` | `5.0` | Дугласа-Пекер |
| **OSRM** | | |
| `OSRM_URL` | — | адрес маршрутизатора; **пусто = чистка и дорисовка выключены** |
| `OSRM_TIMEOUT` | `30s` | потолок на один запрос; бюджет задачи всегда главнее |
| `OSRM_MAX_PARALLEL` | `16` | запросов в полёте на весь сервис, а не на задачу |
| `OSRM_RETRIES` | `2` | повторы на временный отказ; `0` = не повторять |
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

## Защита от перегрузки

| потолок | от чего |
|---|---|
| `MAX_BODY_BYTES` | огромное тело запроса |
| `MAX_DECOMPRESSED_BYTES` | бомба сжатия: маленький архив, огромное содержимое |
| `MAX_POINTS` | дешёвые элементы: размер маленький, точек миллионы |
| `RESOLVE_TIMEOUT` | зависшая обработка |

REST: `MAX_BODY_BYTES` (middleware) → `MAX_DECOMPRESSED_BYTES` и `MAX_POINTS` (codec). gRPC: `GRPC_MAX_RECV_MSG_SIZE` → те же два потолка в codec.

**Оба потолка codec проверяются ВО ВРЕМЯ разбора, а не после.** Байтовый лимит не ограничивает число элементов: самый дешёвый элемент массива — три символа `{},`, и 20 МБ таких дают семь миллионов точек. Поэтому разбор потоковый: элементы читаются по одному, счёт проверяется на каждом.

## Запуск

Прод (нужен Redis):
```bash
cp .env.example .env
docker compose up --build          # ariadne + redis
```
Или локально: `make run` (Redis поднять отдельно). `docker-compose.yml` тянет переменные из `.env`, пробрасывает `8080`/`9090`.

Отладочный синхронный сервер (без Redis и воркеров):
```bash
go run ./cmd/debugserver           # :8080, ручка /v1/routes/resolve-collisions
```

Перед пушем — `make ci`: то же, что стадия `test` в GitLab (`go vet`, `go test -race`, `govulncheck`).

Генерация: `make proto` (нужны `protoc` + плагины), `make swagger` (нужен `swag`).

## Сверка с эталоном

Алгоритм чистки перенесён с прототипа на Python, и перенос проверяется тестами на настоящих треках. Золотые векторы лежат сжатыми в `internal/*/testdata`, снимаются скриптами прототипа (`goldcore.py`, `goldfill.py`, `goldpipe.py`). Запросы к OSRM записаны «плёнкой», поэтому тесты идут офлайн; промах по плёнке валит тест.

| что сверяется | тест | треков |
|---|---|---|
| части ядра: стоянки, веса, правила, цепочка, проверка по дорогам, петли, амнистия | `TestGolden_*` в `internal/core` | 4–8 на правило |
| ядро целиком | `TestGolden_CoreMatchesPrototype` | 8 |
| дорисовка | `TestGoldenFill_MatchesPrototype` | 6 |
| весь конвейер: вход → ответ | `TestGoldenPipe_MatchesPrototype` | 7 |

**Расхождение с эталоном — ошибка переноса, а не повод править эталон.** Единственный случай, когда эталон меняют, — когда меняется не поведение, а название (так было с причинами отказа дорисовки). Порядок:

1. пересобрать вектор без правок и убедиться, что он совпал со старым;
2. поменять в прототипе, пересобрать всё, сверить со старыми с поправкой ровно на переименование;
3. расхождений быть не должно ни в одном поле, кроме переименованных;
4. и только теперь менять в Go.

Названия причин (`accepted`, `detour`, `physics`, `no route`) — это данные, а не текст: поменять их в одном Go нельзя.

## CI/CD (GitLab)

```
git push → .gitlab-ci.yml → image (только если менялся Dockerfile.ci или GO_VERSION)
                          → test  (go vet + go test -race + govulncheck)
                          → build (docker → Registry; по тегу сам, на main — кнопкой)
                          → deploy (webhook, TODO)
```

На обычный пуш бежит одна стадия `test`. Остальные две поднимают `docker:dind`,
а это дороже всех тестов вместе взятых — и раньше они бежали на каждый коммит,
включая правки README. Бесплатных четырёхсот минут в месяц на такое не хватало.

- **Версия Go задаётся в одном месте** — `GO_VERSION` в `.gitlab-ci.yml`; она же уезжает в тег образа для тестов и в `--build-arg` обоих Dockerfile. В `go.mod` версия точная, до заплатки: строку `go` компилятор проверяет всегда, а `toolchain` в официальных образах игнорируется (`GOTOOLCHAIN=local`).
- **`govulncheck` валит сборку** по находкам. Заплатки Go выходят раз в месяц-полтора и закрывают дыры в `crypto/tls` и `net/http`, до которых наш код дотягивается, поэтому красный CI после релиза — штатное дело: поднять `GO_VERSION` и `go.mod`.

## Автор

**Azat Minyazov** — [Telegram](https://t.me/azatmn)
