// Package worker — пул фоновых воркеров. N горутин разбирают очередь задач из
// Redis (taskstore), прогоняют каждую через service.Resolve и пишут результат
// обратно в карточку. С handler не связан — общение только через Redis.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"ariadne/internal/codec"
	"ariadne/internal/geo"
	"ariadne/internal/logger"
	"ariadne/internal/service"
	"ariadne/internal/taskstore"
)

// resolver чистит маршрут. В проде это *service.Service; в тестах подменяется
// фейком, чтобы проверять медленную обработку, ошибки и панику.
type resolver interface {
	Resolve(ctx context.Context, points []geo.Point) (*service.Result, error)
}

// notifier уведомляет внешнюю систему о завершении задачи (Laravel-коллбэк).
// В проде это *callback.Client; в тестах — фейк.
type notifier interface {
	Notify(ctx context.Context, taskKey, status string) error
}

// store — то, что воркеру нужно от хранилища задач. В проде это
// *taskstore.Store.
//
// Интерфейс, а не конкретный тип, ради одной проверки, которую иначе не
// написать: «результат не сохранился — расписку не даём». Поддельный Redis
// умеет ломаться только целиком, и на нём такая проверка проходит по ложной
// причине — не потому, что расписки не было, а потому, что она тоже не
// прошла. Мутация это показала: перенос расписки ПЕРЕД сохранением оставлял
// все тесты зелёными, хотя терял задачу ровно так же, как старая очередь.
type store interface {
	Dequeue(ctx context.Context) (taskstore.Claim, error)
	Trim(ctx context.Context) error
	DropIdleConsumers(ctx context.Context, minIdle time.Duration) (int, error)
	Get(ctx context.Context, taskKey string) (*taskstore.Task, error)
	Update(ctx context.Context, task *taskstore.Task) error
	Ack(ctx context.Context, c taskstore.Claim) error
	Reclaim(ctx context.Context, minIdle time.Duration, maxAttempts int) (requeued, poisoned []string, err error)
}

// Pool — пул воркеров.
type Pool struct {
	store   store
	svc     resolver
	notify  notifier
	logger  *slog.Logger
	workers int
	timeout time.Duration // таймаут на обработку одной задачи
	limits  codec.Limits  // потолки разбора: байты и число точек

	wg sync.WaitGroup
}

// New собирает пул. Реально воркеры стартуют в Start.
func New(store store, svc resolver, notify notifier, logger *slog.Logger, workers int, timeout time.Duration, limits codec.Limits) *Pool {
	return &Pool{
		store:   store,
		svc:     svc,
		notify:  notify,
		logger:  logger,
		workers: workers,
		timeout: timeout,
		limits:  limits,
	}
}

// Start запускает N воркеров и уборщика. Крутятся, пока не отменят ctx.
// Не блокирует.
func (p *Pool) Start(ctx context.Context) {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker(ctx)
	}
	p.wg.Add(1)
	go p.reclaimer(ctx)
}

// reclaimer возвращает в работу задачи, брошенные умершими процессами.
//
// Без него замена очереди сделана наполовину: задача перестала пропадать, но
// и не возвращается — числится за мёртвым процессом до скончания века, а
// клиент ждёт её до конца TTL.
//
// Порог брошенности НЕ отдельная настройка, а удвоенный худший путь обработки
// (`DrainBudget`). Отдельной ручкой её однажды поставят меньше времени
// обработки, и уборщик начнёт отбирать задачи у живых воркеров — двойная
// работа, которую никто не заметит. Меняешь RESOLVE_TIMEOUT — порог едет следом.
func (p *Pool) reclaimer(ctx context.Context) {
	defer p.wg.Done()

	minIdle := 2 * p.DrainBudget()

	// Печатаем порог: он вычисляется из RESOLVE_TIMEOUT, и без этой строки
	// увидеть его можно только в коде. Выросли маршруты, подняли таймаут —
	// в логе сразу видно, каким стал срок.
	p.logger.Info("reclaimer started",
		"every", reclaimInterval, "abandonedAfter", minIdle, "maxAttempts", maxAttempts)

	t := time.NewTicker(reclaimInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.reclaimOnce(ctx, minIdle)
		}
	}
}

func (p *Pool) reclaimOnce(ctx context.Context, minIdle time.Duration) {
	opCtx, cancel := context.WithTimeout(context.Background(), ioTimeout)
	defer cancel()

	// Разбираем ВСЁ, что вернулось, даже вместе с ошибкой.
	//
	// `Reclaim` снимает ядовитую запись со списка выданных сразу и отдаёт
	// номерок нам; если дальше он споткнётся на следующей записи, то вернёт и
	// уже разобранное, и ошибку. Выйти здесь по ошибке значило бы бросить
	// такую карточку в `pending` навсегда: из очереди её сняли, а упавшей не
	// пометили. Ровно тот дефект, ради которого очередь и переписывали.
	requeued, poisoned, err := p.store.Reclaim(opCtx, minIdle, maxAttempts)
	if err != nil {
		p.logger.Error("reclaim failed", "error", err,
			"requeued", len(requeued), "poisoned", len(poisoned))
	}
	if len(requeued) > 0 {
		p.logger.Warn("abandoned tasks returned to queue",
			"count", len(requeued), "taskKeys", requeued)
	}
	for _, key := range poisoned {
		p.failPoisoned(key)
	}

	// Уборка самого потока — здесь же, тем же кругом. Отдельная горутина ради
	// двух команд раз в минуту не нужна.
	if err := p.store.Trim(opCtx); err != nil {
		p.logger.Error("stream trim failed", "error", err)
	}

	// Имена умерших процессов. Порог берём с большим запасом: сосед, который
	// просто ждёт задачу, молчит недолго, а вот запись от процесса, которого
	// нет неделю, — это точно мусор.
	if n, err := p.store.DropIdleConsumers(opCtx, consumerIdleTTL); err != nil {
		p.logger.Error("consumer cleanup failed", "error", err)
	} else if n > 0 {
		p.logger.Info("idle consumers removed", "count", n)
	}
}

// failPoisoned помечает задачу упавшей: её брали maxAttempts раз и ни разу не
// довели до конца. Оставить её в pending значило бы заставить клиента ждать до
// конца TTL ответа, которого не будет.
func (p *Pool) failPoisoned(taskKey string) {
	opCtx, cancel := context.WithTimeout(context.Background(), ioTimeout)
	defer cancel()

	card, err := p.store.Get(opCtx, taskKey)
	if err != nil {
		p.logger.Error("poisoned task: get failed", "taskKey", taskKey, "error", err)
		return
	}
	// Готовую карточку не трогаем. Задача могла быть посчитана и СОХРАНЕНА, а
	// расписка не пройти — Redis моргнул. Тогда её выдадут снова, и после пяти
	// таких невезений уборщик признает её ядовитой. Переписать здесь значило бы
	// объявить неудачей готовый ответ и оставить противоречивую карточку:
	// `failed` с заполненным результатом.
	if card.Status != taskstore.StatusPending {
		p.logger.Warn("poisoned task is already finished, leaving it alone",
			"taskKey", taskKey, "status", card.Status)
		return
	}

	card.Status = taskstore.StatusFailed
	card.Error = fmt.Sprintf("task dropped after %d attempts: processing broke off every time", maxAttempts)
	if err := p.store.Update(opCtx, card); err != nil {
		p.logger.Error("poisoned task: update failed", "taskKey", taskKey, "error", err)
		return
	}
	p.logger.Error("task dropped as poisonous", "taskKey", taskKey, "attempts", maxAttempts)

	// Уведомляем так же, как в обычном пути падения. Без этого выходит тихий
	// тупик: карточка стала `failed`, а Laravel об этом не знает и опрашивает
	// её до конца TTL, чтобы в итоге получить «нет такой задачи».
	notifyCtx, notifyCancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer notifyCancel()
	if err := p.notify.Notify(notifyCtx, card.Key, string(card.Status)); err != nil {
		p.logger.Warn("callback failed", "taskKey", card.Key, "status", card.Status, "error", err)
	}
}

// Shutdown ждёт, пока воркеры допишут текущие задачи (после отмены ctx),
// но не дольше переданного ctx.
func (p *Pool) Shutdown(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		p.logger.Warn("worker pool shutdown timed out, some tasks may be unfinished")
	}
}

// DrainBudget — сколько максимум нужно одной задаче, чтобы гарантированно
// ДОписать результат в Redis: чтение карточки (ioTimeout) + обработка (timeout) +
// запись результата (ioTimeout). На этот срок и надо давать Shutdown при
// выключении, иначе store.Close() оборвёт незавершённую запись (баг A-M2).
// Коллбэк сюда НЕ входит: результат сохраняется до него, а сам коллбэк Redis не
// трогает — его можно бросить. +1с запаса, чтобы бюджет был доказуемо БОЛЬШЕ
// худшего пути, а не впритык: иначе на самом пороге Shutdown мог бы выйти по
// таймауту ровно в момент записи и Close() обогнал бы её (F-1).
func (p *Pool) DrainBudget() time.Duration {
	return p.timeout + 2*ioTimeout + time.Second
}

// worker — цикл одного воркера: ждёт задачу из очереди и обрабатывает.
func (p *Pool) worker(ctx context.Context) {
	defer p.wg.Done()
	for {
		if ctx.Err() != nil { // сервис останавливают
			return
		}

		claim, err := p.store.Dequeue(ctx)
		if errors.Is(err, taskstore.ErrNoTask) {
			continue // очередь пустовала — новый круг (и проверка ctx сверху)
		}
		if err != nil {
			if ctx.Err() != nil { // Dequeue прервался из-за остановки
				return
			}
			p.logger.Error("dequeue failed", "error", err)
			continue
		}

		p.process(claim)
	}
}

// reclaimInterval — как часто уборщик ищет брошенные задачи.
//
// Раз в минуту: задача, которую бросил умерший процесс, всё равно уже
// просрочена на величину порога, и лишняя минута ничего не меняет. var —
// чтобы тесты не ждали минуту.
var reclaimInterval = time.Minute

// maxAttempts — сколько раз задачу можно выдать, прежде чем признать её
// ядовитой.
//
// Ядовитая — та, что роняет обработку сама по себе: пятикратный обход по
// кругу выглядит как «сервис постоянно перезапускается» и не объясняет
// ничего. Пять — с запасом на случайные беды (моргнула сеть, выключили
// контейнер), но заведомо конечно.
const maxAttempts = 5

// consumerIdleTTL — сколько имя процесса живёт в группе без дела.
//
// Каждый запуск регистрируется под своим именем, при остановке имя остаётся.
// Сутки — с запасом: живой процесс за это время сто раз возьмёт задачу, а
// имя от контейнера, которого нет со вчера, точно мусор.
const consumerIdleTTL = 24 * time.Hour

// ioTimeout — таймаут на чтение/запись карточки в Redis. Отдельный от таймаута
// обработки задачи. var (а не const), чтобы тесты могли занижать его.
var ioTimeout = 5 * time.Second

// notifyTimeout — общий бюджет на коллбэк (с учётом ретраев), чтобы залипший
// Laravel не держал воркер бесконечно. var — чтобы тесты могли занижать.
var notifyTimeout = 30 * time.Second

// process обрабатывает одну задачу. КАЖДЫЙ контекст создаётся НЕПОСРЕДСТВЕННО
// перед своей операцией, чтобы его таймер не «тикал» во время чужих операций:
//
//	getCtx   — чтение карточки из Redis (ioTimeout);
//	procCtx  — обработка маршрута (p.timeout);
//	writeCtx — запись результата в Redis (ioTimeout). Создаётся ПОСЛЕ обработки,
//	           поэтому не истекает, даже если обработка шла долго — статус
//	           done/failed гарантированно записывается.
//
// Все из context.Background() — не завязаны на выключение, чтобы начатую задачу
// доделать. Паника в обработке ловится в runFill и превращается в failed (задача
// не зависает в pending); внешний recover — последняя страховка воркера на случай
// паники в чтении/записи/коллбэке.
func (p *Pool) process(claim taskstore.Claim) {
	taskKey := claim.TaskKey

	// Логгер с номером задачи — и в свои записи, и В КОНТЕКСТ обработки.
	//
	// Второе важнее. Конвейер про воркера не знает и берёт логгер из контекста
	// (`logger.FromContext`); контексты здесь создаются из `Background`, и без
	// этой строки внутри пусто — записи уходили в стандартный поток: другим
	// форматом, в другой файл и без номера задачи. Разобрать по такому логу
	// ночной инцидент нельзя: видно «стадия отработала», но не видно, о каком
	// маршруте речь.
	log := p.logger.With("taskKey", taskKey)

	defer func() {
		if r := recover(); r != nil {
			log.Error("panic in worker", "panic", r, "stack", string(debug.Stack()))
		}
	}()

	base := logger.ToContext(context.Background(), log)

	getCtx, getCancel := context.WithTimeout(base, ioTimeout)
	card, err := p.store.Get(getCtx, taskKey)
	getCancel()
	if err != nil {
		// Без расписки: карточку не прочитали, работа не сделана — пусть
		// задачу вернут в работу, а не потеряют.
		log.Error("get task failed", "error", err)
		return
	}

	// runFill (не fill напрямую): паника внутри не проскочит мимо procCancel и
	// мимо пометки failed — вернётся ошибкой, задача завершится штатно.
	procCtx, procCancel := context.WithTimeout(base, p.timeout)
	fillErr := p.runFill(procCtx, card)
	procCancel()

	if fillErr != nil {
		card.Status = taskstore.StatusFailed
		card.Error = fillErr.Error()
		log.Warn("task failed", "error", fillErr)
	} else {
		card.Status = taskstore.StatusDone
	}

	writeCtx, writeCancel := context.WithTimeout(base, ioTimeout)
	defer writeCancel()
	if err := p.store.Update(writeCtx, card); err != nil {
		// Расписки НЕ даём: задача остаётся числиться выданной, и её вернут в
		// работу. Раньше номерок здесь был уже потерян навсегда, а карточка
		// висела в pending до конца TTL.
		log.Error("update task failed", "error", err)
		return // результат не сохранён — Laravel не уведомляем
	}

	// Результат записан — только теперь расписываемся. Ошибка расписки не
	// страшна: задачу выдадут снова, она пересчитается и перезапишет тот же
	// результат.
	ackCtx, ackCancel := context.WithTimeout(base, ioTimeout)
	defer ackCancel()
	if err := p.store.Ack(ackCtx, claim); err != nil {
		log.Error("ack task failed", "error", err)
	}

	// Уведомляем Laravel. Свой ctx из Background — коллбэк добиваем независимо
	// от shutdown. Ошибку только логируем: задача уже сохранена, статус заберут
	// и опросом GET /v1/tasks/{taskKey}.
	notifyCtx, notifyCancel := context.WithTimeout(base, notifyTimeout)
	defer notifyCancel()
	if err := p.notify.Notify(notifyCtx, card.Key, string(card.Status)); err != nil {
		log.Warn("callback failed", "status", card.Status, "error", err)
	}
}

// runFill вызывает fill и превращает панику в обычную ошибку. Без этого паника
// в обработке (например кривые данные в новой стадии) прошла бы мимо пометки
// failed, и задача зависла бы в pending до TTL: номерок из очереди уже снят
// (BRPOP без копии), повторно её никто не возьмёт. Отдавая ошибку, направляем
// задачу штатным путём failed → Update → callback. Стек — в лог, клиенту уходит
// короткое "panic: …".
func (p *Pool) runFill(ctx context.Context, card *taskstore.Task) (err error) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("panic during processing", "taskKey", card.Key, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return p.fill(ctx, card)
}

// fill чистит маршрут и заполняет поля результата прямо в card.
// При ошибке возвращает её — process пометит задачу failed.
func (p *Pool) fill(ctx context.Context, card *taskstore.Task) error {
	points, err := codec.Decode(card.Input, p.limits)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}

	res, err := p.svc.Resolve(ctx, points)
	if err != nil {
		return fmt.Errorf("resolve: %w", err)
	}

	compressed, err := codec.Encode(res.Points)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}

	card.Result = compressed
	card.LengthMeters = res.LengthMeters
	card.Debug = res.Stats

	// Оговорка к километражу. Раньше терялась ровно здесь: конвейер знал, что
	// результат неполный, сервис это отдавал, а в карточку не клалось — и
	// клиент получал заниженную цифру со статусом «готово» и без единого слова.
	card.Degraded = res.Degraded
	card.Warnings = res.Warnings
	return nil
}
