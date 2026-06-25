// Package taskstore хранит задачи и результаты обработки в Redis.
// Пока здесь только подключение к Redis и проверка связи —
// методы работы с задачами (поставить в очередь, прочитать результат) добавятся дальше.
package taskstore

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Store — обёртка над клиентом Redis.
type Store struct {
	rdb *redis.Client
}

// New создаёт Store. Само соединение ленивое: реальную связь устанавливает
// первый запрос, поэтому на старте обязательно вызвать Ping.
func New(addr string, db int, password string) *Store {
	return &Store{
		rdb: redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       db,
		}),
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
