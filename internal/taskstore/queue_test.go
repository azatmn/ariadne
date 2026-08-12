package taskstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnqueueDequeue_FIFO(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))

	require.NoError(t, s.Enqueue(ctx, "task-1"))
	require.NoError(t, s.Enqueue(ctx, "task-2"))

	c1, err := s.Dequeue(ctx)
	require.NoError(t, err)
	assert.Equal(t, "task-1", c1.TaskKey)

	c2, err := s.Dequeue(ctx)
	require.NoError(t, err)
	assert.Equal(t, "task-2", c2.TaskKey)
}

// Главное свойство новой очереди: выданная задача НЕ исчезает.
//
// Раньше номерок снимался с очереди насовсем, и если воркер умирал между
// выдачей и сохранением результата — задача пропадала навсегда, а карточка
// висела в pending до конца TTL. Теперь она числится за тем, кто её взял,
// пока он не распишется.
func TestDequeue_TaskStaysClaimedUntilAck(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))
	require.NoError(t, s.Enqueue(ctx, "task-1"))

	claim, err := s.Dequeue(ctx)
	require.NoError(t, err)

	n, err := s.Claimed(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "выданная и неподтверждённая задача обязана числиться за воркером")

	require.NoError(t, s.Ack(ctx, claim))

	n, err = s.Claimed(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "после расписки задача больше ни за кем не числится")
}

// Одну и ту же задачу двум воркерам сразу выдавать нельзя.
func TestDequeue_NoDoubleDelivery(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))
	require.NoError(t, s.Enqueue(ctx, "only-one"))

	first, err := s.Dequeue(ctx)
	require.NoError(t, err)
	assert.Equal(t, "only-one", first.TaskKey)

	_, err = s.Dequeue(ctx)
	require.ErrorIs(t, err, ErrNoTask, "вторая выдача той же задачи недопустима")
}

// Пустая очередь — не ошибка: воркер должен спокойно зайти на новый круг и
// заодно проверить, не пора ли останавливаться.
func TestDequeue_EmptyQueueReturnsErrNoTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))

	_, err := s.Dequeue(ctx)
	require.ErrorIs(t, err, ErrNoTask)
}

// Повторный вызов EnsureQueue обязан быть безобидным: сервис перезапускают,
// а группа потребителей на этот момент уже создана.
func TestEnsureQueue_Idempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	require.NoError(t, s.EnsureQueue(ctx))
	require.NoError(t, s.EnsureQueue(ctx), "повторный вызов не должен падать")

	require.NoError(t, s.Enqueue(ctx, "after-restart"))
	claim, err := s.Dequeue(ctx)
	require.NoError(t, err)
	assert.Equal(t, "after-restart", claim.TaskKey)
}

// Без группы потребителей очередь не работает, и молчать об этом нельзя:
// забытый вызов обязан быть виден сразу, а не выглядеть как «задач нет».
func TestDequeue_WithoutEnsureQueueFailsLoudly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, err := s.Dequeue(ctx)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoTask, "отсутствие группы — это поломка, а не пустая очередь")
}

func init() {
	// В тестах ждать по пять секунд на каждой пустой очереди незачем.
	blockTimeout = 50 * time.Millisecond
}

// --- уборщик брошенных задач ---------------------------------------------

// Живую задачу отбирать нельзя. Порог берём заведомо больше её возраста —
// так проверка не зависит от часов и не «моргает» на загруженной машине.
func TestReclaim_LeavesFreshClaimAlone(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))
	require.NoError(t, s.Enqueue(ctx, "working"))

	_, err := s.Dequeue(ctx)
	require.NoError(t, err)

	requeued, poisoned, err := s.Reclaim(ctx, time.Hour, 5)
	require.NoError(t, err)
	assert.Empty(t, requeued, "задача в работе минуту назад — не брошенная")
	assert.Empty(t, poisoned)

	n, err := s.Claimed(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "она как числилась за воркером, так и должна числиться")
}

// Брошенная задача возвращается в очередь, и её может взять другой воркер.
//
// Это и есть смысл уборщика: процесс умер между выдачей и сохранением, никто
// об этом не узнал — но задача не должна остаться висеть навсегда.
func TestReclaim_RequeuesAbandonedTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))
	require.NoError(t, s.Enqueue(ctx, "abandoned"))

	_, err := s.Dequeue(ctx)
	require.NoError(t, err)

	// Порог 0 — «брошено всё, что выдано»: так проверка не зависит от часов.
	requeued, poisoned, err := s.Reclaim(ctx, 0, 5)
	require.NoError(t, err)
	assert.Equal(t, []string{"abandoned"}, requeued)
	assert.Empty(t, poisoned)

	again, err := s.Dequeue(ctx)
	require.NoError(t, err)
	assert.Equal(t, "abandoned", again.TaskKey, "вернувшуюся задачу обязан получить следующий воркер")
}

// Задача, которую брали слишком много раз, — ядовитая: она роняет обработку
// сама по себе, и гонять её по кругу бессмысленно. Уборщик перестаёт её
// возвращать и отдаёт вызывающему, чтобы тот пометил её упавшей.
func TestReclaim_StopsPoisonousTaskAfterLimit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))
	require.NoError(t, s.Enqueue(ctx, "poison"))

	const limit = 3
	for range limit {
		_, err := s.Dequeue(ctx)
		require.NoError(t, err)
		_, poisoned, err := s.Reclaim(ctx, 0, limit)
		require.NoError(t, err)
		if len(poisoned) > 0 {
			assert.Equal(t, []string{"poison"}, poisoned)
			n, err := s.Claimed(ctx)
			require.NoError(t, err)
			assert.Zero(t, n, "ядовитую задачу больше не держим в выданных")
			return
		}
	}
	t.Fatalf("после %d попыток задача обязана быть признана ядовитой", limit)
}

// Подтверждённую задачу уборщик не трогает: она доведена до конца.
func TestReclaim_IgnoresAckedTask(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))
	require.NoError(t, s.Enqueue(ctx, "finished"))

	claim, err := s.Dequeue(ctx)
	require.NoError(t, err)
	require.NoError(t, s.Ack(ctx, claim))

	requeued, poisoned, err := s.Reclaim(ctx, 0, 5)
	require.NoError(t, err)
	assert.Empty(t, requeued)
	assert.Empty(t, poisoned)
}

// Пустая очередь — уборщику просто нечего делать.
func TestReclaim_EmptyQueueIsNoop(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))

	requeued, poisoned, err := s.Reclaim(ctx, 0, 5)
	require.NoError(t, err)
	assert.Empty(t, requeued)
	assert.Empty(t, poisoned)
}
