package taskstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// queueKey — Redis-список, который служит очередью номерков задач.
const queueKey = "tasks:queue"

// blockTimeout — насколько BRPOP «висит» за один заход. По истечении возвращает
// ErrNoTask, и воркер может проверить ctx (не пора ли останавливаться) и зайти снова.
const blockTimeout = 5 * time.Second

// ErrNoTask — за время ожидания в очереди ничего не появилось (тайм-аут BRPOP).
var ErrNoTask = errors.New("taskstore: no task in queue")

// Enqueue кладёт номерок задачи в очередь.
func (s *Store) Enqueue(ctx context.Context, taskKey string) error {
	if err := s.rdb.LPush(ctx, queueKey, taskKey).Err(); err != nil {
		return fmt.Errorf("taskstore: enqueue: %w", err)
	}
	return nil
}

// Dequeue ждёт и забирает номерок из очереди (BRPOP). Блокируется не дольше
// blockTimeout; если за это время ничего не пришло — возвращает ErrNoTask.
func (s *Store) Dequeue(ctx context.Context) (taskKey string, err error) {
	res, err := s.rdb.BRPop(ctx, blockTimeout, queueKey).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrNoTask
	}
	if err != nil {
		return "", fmt.Errorf("taskstore: dequeue: %w", err)
	}
	// res = [ключ_списка, значение]; значение — наш taskKey.
	return res[1], nil
}
