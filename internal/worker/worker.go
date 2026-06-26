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

// Pool — пул воркеров.
type Pool struct {
	store                *taskstore.Store
	svc                  resolver
	logger               *slog.Logger
	workers              int
	timeout              time.Duration // таймаут на обработку одной задачи
	maxDecompressedBytes int64         // лимит распаковки для codec.Decode

	wg sync.WaitGroup
}

// New собирает пул. Реально воркеры стартуют в Start.
func New(store *taskstore.Store, svc resolver, logger *slog.Logger, workers int, timeout time.Duration, maxDecompressedBytes int64) *Pool {
	return &Pool{
		store:                store,
		svc:                  svc,
		logger:               logger,
		workers:              workers,
		timeout:              timeout,
		maxDecompressedBytes: maxDecompressedBytes,
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

// worker — цикл одного воркера: ждёт задачу из очереди и обрабатывает.
func (p *Pool) worker(ctx context.Context) {
	defer p.wg.Done()
	for {
		if ctx.Err() != nil { // сервис останавливают
			return
		}

		taskKey, err := p.store.Dequeue(ctx)
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

		p.process(taskKey)
	}
}

// ioTimeout — таймаут на чтение/запись карточки в Redis. Отдельный от таймаута
// обработки задачи. var (а не const), чтобы тесты могли занижать его.
var ioTimeout = 5 * time.Second

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
// доделать. recover не даёт кривой задаче уронить воркер.
func (p *Pool) process(taskKey string) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("panic in worker", "taskKey", taskKey, "panic", r, "stack", string(debug.Stack()))
		}
	}()

	getCtx, getCancel := context.WithTimeout(context.Background(), ioTimeout)
	card, err := p.store.Get(getCtx, taskKey)
	getCancel()
	if err != nil {
		p.logger.Error("get task failed", "taskKey", taskKey, "error", err)
		return
	}

	procCtx, procCancel := context.WithTimeout(context.Background(), p.timeout)
	fillErr := p.fill(procCtx, card)
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
		p.logger.Error("update task failed", "taskKey", taskKey, "error", err)
	}
}

// fill чистит маршрут и заполняет поля результата прямо в card.
// При ошибке возвращает её — process пометит задачу failed.
func (p *Pool) fill(ctx context.Context, card *taskstore.Task) error {
	points, err := codec.Decode(card.Input, p.maxDecompressedBytes)
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
	return nil
}
