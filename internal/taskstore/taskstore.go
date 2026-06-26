// Package taskstore хранит задачи и результаты обработки в Redis.
// Пока здесь только подключение к Redis и проверка связи —
// методы работы с задачами (поставить в очередь, прочитать результат) добавятся дальше.
package taskstore

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store — обёртка над клиентом Redis. ttl — время жизни карточки задачи.
type Store struct {
	rdb *redis.Client
	ttl time.Duration
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
		ttl: ttl,
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
