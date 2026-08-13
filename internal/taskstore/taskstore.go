// Package taskstore хранит задачи и результаты обработки в Redis.
// Пока здесь только подключение к Redis и проверка связи —
// методы работы с задачами (поставить в очередь, прочитать результат) добавятся дальше.
package taskstore

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store — обёртка над клиентом Redis. ttl — время жизни карточки задачи.
type Store struct {
	rdb *redis.Client
	ttl time.Duration

	// consumer — имя этого процесса в группе потребителей очереди.
	// Постоянно на всё время жизни процесса: по нему Redis ведёт список
	// выданных, но не подтверждённых задач.
	consumer string

	// ensured — EnsureQueue уже отработал в этом процессе.
	//
	// По ней Dequeue отличает два одинаковых с виду отказа: очередь не
	// готовили вовсе (ошибка сборки сервиса, падаем громко) и очередь
	// готовили, но группа потом пропала вместе с данными Redis (поднимаем
	// сами, без перезапуска).
	ensured atomic.Bool
}

// New создаёт Store. Само соединение ленивое: реальную связь устанавливает
// первый запрос, поэтому на старте обязательно вызвать Ping.
func New(addr string, db int, password string, ttl time.Duration) *Store {
	return &Store{
		rdb: redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       db,
		}),
		ttl:      ttl,
		consumer: consumerName(),
	}
}

// Ping проверяет доступность Redis. Вызывать на старте сервиса —
// если Redis не поднят, лучше упасть сразу с понятной ошибкой.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("taskstore: ping redis: %w", err)
	}
	return nil
}

// Close закрывает пул соединений с Redis.
func (s *Store) Close() error {
	return s.rdb.Close()
}
