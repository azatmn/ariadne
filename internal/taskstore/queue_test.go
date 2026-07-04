package taskstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnqueueDequeue_FIFO(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	require.NoError(t, s.Enqueue(ctx, "task-1"))
	require.NoError(t, s.Enqueue(ctx, "task-2"))

	// кто первым зашёл — первым и вышел (LPUSH слева, BRPOP справа)
	k1, err := s.Dequeue(ctx)
	require.NoError(t, err)
	assert.Equal(t, "task-1", k1)

	k2, err := s.Dequeue(ctx)
	require.NoError(t, err)
	assert.Equal(t, "task-2", k2)
}
