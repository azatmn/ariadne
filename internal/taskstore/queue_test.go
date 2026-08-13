package taskstore

import (
	"context"
	"fmt"
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

// --- обрезка потока и уборка потребителей ---------------------------------

// Обрезка не смеет трогать невыполненную задачу.
//
// Поток не самоочищается: подтверждённые записи остаются в нём навсегда, и без
// обрезки он растёт бесконечно. Но обрезать по длине опасно — Redis выкидывает
// самые старые, не глядя, доведена задача до конца или ещё считается. Резать
// можно только то, что старше самой старой невыполненной.
func TestTrim_KeepsUnacknowledgedEntries(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))

	// Первая задача берётся в работу и НЕ подтверждается.
	require.NoError(t, s.Enqueue(ctx, "in-progress"))
	claim, err := s.Dequeue(ctx)
	require.NoError(t, err)

	// Поверх неё сыпется много новых.
	for i := range 50 {
		require.NoError(t, s.Enqueue(ctx, fmt.Sprintf("later-%d", i)))
	}

	require.NoError(t, s.Trim(ctx))

	n, err := s.Claimed(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "невыполненная задача обязана пережить обрезку")

	// И она по-прежнему подтверждается — запись на месте, а не выброшена.
	require.NoError(t, s.Ack(ctx, claim))
}

// Когда невыполненных нет, обрезка убирает разобранное: ради неё всё и затеяно.
func TestTrim_RemovesFinishedEntries(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))

	for i := range 20 {
		require.NoError(t, s.Enqueue(ctx, fmt.Sprintf("done-%d", i)))
		c, err := s.Dequeue(ctx)
		require.NoError(t, err)
		require.NoError(t, s.Ack(ctx, c))
	}

	before, err := s.streamLen(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(20), before)

	require.NoError(t, s.Trim(ctx))

	after, err := s.streamLen(ctx)
	require.NoError(t, err)
	assert.Less(t, after, before, "разобранные записи должны уйти: %d → %d", before, after)
}

// Имена процессов в группе не должны копиться вечно.
//
// Каждый запуск сервиса регистрируется под своим именем, а при остановке имя
// остаётся. За полгода перезапусков их накапливаются сотни — мусор в общем
// Redis, который сам не убирается.
func TestDropIdleConsumers_RemovesOnlyIdleAndEmpty(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))

	// ЧУЖОЙ процесс берёт задачу и не подтверждает её.
	//
	// Чужой, а не этот, — принципиально. Свой защищён отдельной проверкой «это
	// я», и на нём не видно, работает ли главное правило: «не трогать того, за
	// кем числится невыполненная задача». Мутация это показала — снятие
	// проверки на невыполненные оставляло тест зелёным.
	other := newStoreOn(t, s, "other-process")
	require.NoError(t, s.Enqueue(ctx, "held"))
	_, err := other.Dequeue(ctx)
	require.NoError(t, err)

	// Порог 0 — «простаивают все»: так проверка не зависит от часов.
	dropped, err := s.DropIdleConsumers(ctx, 0)
	require.NoError(t, err)
	assert.Zero(t, dropped, "потребителя с невыполненной задачей удалять нельзя")

	n, err := s.Claimed(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "и его задача обязана остаться выданной")
}

// А вот молчащего и пустого — можно и нужно: ради этого всё и затевалось.
func TestDropIdleConsumers_RemovesDeadOne(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnsureQueue(ctx))

	// Чужой процесс сделал свою работу и ушёл: задача взята, доведена до конца
	// и подтверждена. За ним не числится ничего — только имя в группе.
	other := newStoreOn(t, s, "dead-process")
	require.NoError(t, s.Enqueue(ctx, "finished-by-other"))
	c, err := other.Dequeue(ctx)
	require.NoError(t, err)
	require.NoError(t, other.Ack(ctx, c))

	dropped, err := s.DropIdleConsumers(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, dropped, "имя ушедшего процесса обязано убраться")
}

// newStoreOn — второй Store на том же Redis, но с другим именем процесса.
// Нужен, чтобы отличать «свой» от «чужого»: на своём половина правил не
// проверяется, потому что его защищает отдельная проверка.
func newStoreOn(t *testing.T, src *Store, consumer string) *Store {
	t.Helper()
	return &Store{rdb: src.rdb, ttl: src.ttl, consumer: consumer}
}
