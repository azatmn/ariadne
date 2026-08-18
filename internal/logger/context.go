// Package logger переносит логгер запроса через context.
//
// Логгер кладёт middleware, добавив к нему идентификатор запроса; дальше по
// цепочке его достают конвейер, клиент маршрутизатора и воркер. Без этого
// пришлось бы протаскивать *slog.Logger отдельным аргументом через каждую
// функцию до самого низа.
package logger

import (
	"context"
	"log/slog"
)

// ctxKey — приватный тип ключа. Пустая структура своего типа гарантирует, что
// никакой другой пакет в тот же context под этим ключом ничего не запишет:
// совпасть можно было бы только по типу, а тип неэкспортирован.
type ctxKey struct{}

// ToContext кладёт логгер в context.
func ToContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext достаёт логгер, положенный ToContext. Если его там нет,
// возвращает slog.Default() — вызывающему не нужно проверять на nil, и код
// одинаково работает в проде и в тестах, где context пустой.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
