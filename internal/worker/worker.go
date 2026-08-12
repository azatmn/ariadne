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
	Get(ctx context.Context, taskKey string) (*taskstore.Task, error)
	Update(ctx context.Context, task *taskstore.Task) error
	Ack(ctx context.Context, c taskstore.Claim) error
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

// Start запускает N воркеров. Они крутятся, пока не отменят ctx. Не блокирует.
func (p *Pool) Start(ctx context.Context) {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker(ctx)
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
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("panic in worker", "taskKey", taskKey, "panic", r, "stack", string(debug.Stack()))
		}
	}()

	getCtx, getCancel := context.WithTimeout(context.Background(), ioTimeout)
	card, err := p.store.Get(getCtx, taskKey)
	getCancel()
	if err != nil {
		// Без расписки: карточку не прочитали, работа не сделана — пусть
		// задачу вернут в работу, а не потеряют.
		p.logger.Error("get task failed", "taskKey", taskKey, "error", err)
		return
	}

	// runFill (не fill напрямую): паника внутри не проскочит мимо procCancel и
	// мимо пометки failed — вернётся ошибкой, задача завершится штатно.
	procCtx, procCancel := context.WithTimeout(context.Background(), p.timeout)
	fillErr := p.runFill(procCtx, card)
	procCancel()

	if fillErr != nil {
		card.Status = taskstore.StatusFailed
		card.Error = fillErr.Error()
		p.logger.Warn("task failed", "taskKey", taskKey, "error", fillErr)
	} else {
		card.Status = taskstore.StatusDone
	}

	writeCtx, writeCancel := context.WithTimeout(context.Background(), ioTimeout)
	defer writeCancel()
	if err := p.store.Update(writeCtx, card); err != nil {
		// Расписки НЕ даём: задача остаётся числиться выданной, и её вернут в
		// работу. Раньше номерок здесь был уже потерян навсегда, а карточка
		// висела в pending до конца TTL.
		p.logger.Error("update task failed", "taskKey", taskKey, "error", err)
		return // результат не сохранён — Laravel не уведомляем
	}

	// Результат записан — только теперь расписываемся. Ошибка расписки не
	// страшна: задачу выдадут снова, она пересчитается и перезапишет тот же
	// результат.
	ackCtx, ackCancel := context.WithTimeout(context.Background(), ioTimeout)
	defer ackCancel()
	if err := p.store.Ack(ackCtx, claim); err != nil {
		p.logger.Error("ack task failed", "taskKey", taskKey, "error", err)
	}

	// Уведомляем Laravel. Свой ctx из Background — коллбэк добиваем независимо
	// от shutdown. Ошибку только логируем: задача уже сохранена, статус заберут
	// и опросом GET /v1/tasks/{taskKey}.
	notifyCtx, notifyCancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer notifyCancel()
	if err := p.notify.Notify(notifyCtx, card.Key, string(card.Status)); err != nil {
		p.logger.Warn("callback failed", "taskKey", card.Key, "status", card.Status, "error", err)
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
