package taskstore

import (
	"time"

	"ariadne/internal/pipeline"
)

// Status — стадия задачи.
type Status string

const (
	StatusPending Status = "pending" // создана, ждёт в очереди или обрабатывается
	StatusDone    Status = "done"    // готова, есть результат
	StatusFailed  Status = "failed"  // упала, есть Error
)

// Task — карточка задачи. Лежит в Redis под ключом task:{Key} как JSON, TTL 1 час.
// Input есть с момента приёма; результат (Result/LengthMeters/Debug) или Error
// дописываются воркером по завершении.
type Task struct {
	Key       string    `json:"key"`    // taskKey (uuid)
	Status    Status    `json:"status"` // pending / done / failed
	Input     string    `json:"input"`  // присланный сжатый маршрут (routeCompressed)
	CreatedAt time.Time `json:"createdAt"`

	// Заполняются при Status == StatusDone:
	Result       string                `json:"result,omitempty"`       // очищенный маршрут в сжатом виде (routeCompressed)
	LengthMeters float64               `json:"lengthMeters,omitempty"` // длина результата, метры
	Debug        []pipeline.StageStats `json:"debug,omitempty"`        // разбор по стадиям

	// Заполняется при Status == StatusFailed:
	Error string `json:"error,omitempty"`
}
