package taskstore

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testStore поднимает miniredis (Redis в памяти) и возвращает Store на него.
func testStore(t *testing.T) *Store {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	return New(mr.Addr(), 0, "", time.Hour)
}

func TestSaveNewAndGet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task := &Task{Key: "abc", Status: StatusPending, Input: "data", CreatedAt: time.Now()}
	ok, err := s.SaveNew(ctx, task)
	require.NoError(t, err)
	assert.True(t, ok)

	got, err := s.Get(ctx, "abc")
	require.NoError(t, err)
	assert.Equal(t, "abc", got.Key)
	assert.Equal(t, StatusPending, got.Status)
	assert.Equal(t, "data", got.Input)
}

func TestSaveNew_DuplicateKeyReturnsFalse(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task := &Task{Key: "dup", Status: StatusPending}
	ok, err := s.SaveNew(ctx, task)
	require.NoError(t, err)
	require.True(t, ok)

	// тот же ключ — NX не даёт перезаписать
	ok, err = s.SaveNew(ctx, task)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestGet_NotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.Get(context.Background(), "missing")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestUpdate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task := &Task{Key: "u", Status: StatusPending}
	_, err := s.SaveNew(ctx, task)
	require.NoError(t, err)

	task.Status = StatusDone
	task.Result = "compressed"
	task.LengthMeters = 123

	require.NoError(t, s.Update(ctx, task))

	got, err := s.Get(ctx, "u")
	require.NoError(t, err)
	assert.Equal(t, StatusDone, got.Status)
	assert.Equal(t, "compressed", got.Result)
	assert.Equal(t, 123.0, got.LengthMeters)
}
