package logger

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToFromContext(t *testing.T) {
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := ToContext(context.Background(), l)
	got := FromContext(ctx)

	assert.Equal(t, l, got, "FromContext should return the logger set by ToContext")
}

func TestFromContextFallback(t *testing.T) {
	got := FromContext(context.Background())
	assert.NotNil(t, got, "FromContext should return slog.Default() when no logger in context")
}
