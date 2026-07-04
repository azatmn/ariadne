package taskstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound — задачи с таким ключом нет (не создавали или протухла по TTL).
var ErrNotFound = errors.New("taskstore: task not found")

// cardKey — ключ Redis для карточки задачи: task:{taskKey}.
func cardKey(taskKey string) string {
	return "task:" + taskKey
}

// SaveNew кладёт НОВУЮ карточку под task:{Key} с TTL стора, командой SET NX.
// Если ключ уже занят (мизерный шанс коллизии uuid) — возвращает false:
// тогда вызывающий генерит новый Key и повторяет.
func (s *Store) SaveNew(ctx context.Context, task *Task) (bool, error) {
	data, err := json.Marshal(task)
	if err != nil {
		return false, fmt.Errorf("taskstore: marshal task: %w", err)
	}
	ok, err := s.rdb.SetNX(ctx, cardKey(task.Key), data, s.ttl).Result()
	if err != nil {
		return false, fmt.Errorf("taskstore: save task: %w", err)
	}
	return ok, nil
}

// Get читает карточку по ключу. Если её нет (или протухла) — ErrNotFound.
func (s *Store) Get(ctx context.Context, taskKey string) (*Task, error) {
	data, err := s.rdb.Get(ctx, cardKey(taskKey)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("taskstore: get task: %w", err)
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, fmt.Errorf("taskstore: unmarshal task: %w", err)
	}
	return &task, nil
}

// Update перезаписывает существующую карточку (воркер пишет результат).
// Обычный SET перезаписывает значение и заново ставит TTL — карточка живёт
// свой час с момента обновления.
func (s *Store) Update(ctx context.Context, task *Task) error {
	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("taskstore: marshal task: %w", err)
	}
	if err := s.rdb.Set(ctx, cardKey(task.Key), data, s.ttl).Err(); err != nil {
		return fmt.Errorf("taskstore: update task: %w", err)
	}
	return nil
}
