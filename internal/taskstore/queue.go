package taskstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Очередь задач на потоке Redis (stream), а не на списке.
//
// Список делал ровно одно: отдавал номерок и тут же о нём забывал. Между
// выдачей и сохранением результата задача не существовала нигде, кроме памяти
// воркера, — и любая беда в этом промежутке уносила её навсегда. Карточка
// оставалась в `pending` до конца TTL, клиент опрашивал её час и не дожидался
// ничего. Три случая на одну причину: не прочиталась карточка, не записался
// результат, умер сам процесс.
//
// Поток ведёт список выданного сам. Задача числится за тем, кто её взял, пока
// он не распишется (`Ack`); неподтверждённые видны через `XPENDING` и
// возвращаются в работу через `XAUTOCLAIM`.
//
// Отдельно — счётчик выдач на каждой записи. Своими руками такое не сделать
// дёшево, а без него ядовитая задача (роняет процесс на каждой попытке) ходит
// по кругу вечно и выглядит как «сервис сам перезапускается».
const (
	// streamKey — сам поток номерков.
	streamKey = "tasks:stream"

	// groupName — группа потребителей. Все воркеры всех процессов читают в
	// одной группе: запись достаётся ровно одному из них.
	groupName = "tasks:workers"

	// streamMaxLen — потолок длины потока.
	//
	// Подтверждённые записи из потока НЕ исчезают: `XACK` снимает их только со
	// списка выданного. Без обрезки поток растёт вечно и съедает память
	// общего Redis. Сто тысяч — это запас на порядок больше суточного объёма,
	// а обрезка приблизительная (`~`), чтобы Redis не тратил время на точное
	// соблюдение границы.
	streamMaxLen = 100_000

	// reclaimBatch — сколько брошенных записей забираем за один заход.
	// Потолок нужен, чтобы уборщик не выгребал разом всё после долгой аварии
	// и не заваливал воркеров: остаток заберётся на следующем круге.
	reclaimBatch = 100
)

// blockTimeout — насколько Dequeue «висит» за один заход. По истечении
// возвращает ErrNoTask, и воркер может проверить ctx (не пора ли
// останавливаться) и зайти снова. var — чтобы тесты не ждали по пять секунд.
var blockTimeout = 5 * time.Second

// ErrNoTask — за время ожидания в очереди ничего не появилось.
var ErrNoTask = errors.New("taskstore: no task in queue")

// Claim — выданная задача: номерок и расписка о выдаче.
//
// ID — идентификатор записи в потоке. Без него нельзя расписаться за
// выполнение, поэтому он и едет вместе с номерком, а не выводится из него.
type Claim struct {
	TaskKey string
	ID      string
}

// consumerName — имя этого процесса в группе потребителей.
//
// Имя нужно самому Redis: он ведёт список выданного отдельно по каждому
// потребителю. Имя процесса, а не воркера: воркеры одного процесса живут и
// умирают вместе, и разделять их незачем. Случайная часть обязательна — два
// контейнера с одинаковым именем растащили бы чужие задачи как свои.
func consumerName() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), uuid.NewString()[:8])
}

// EnsureQueue создаёт поток и группу потребителей, если их ещё нет.
//
// Вызывать на старте сервиса, рядом с Ping. Отдельным шагом, а не втихую при
// первом обращении: забытый вызов обязан быть виден сразу и громко, а не
// выглядеть как «задач почему-то нет».
func (s *Store) EnsureQueue(ctx context.Context) error {
	// MKSTREAM создаёт поток, если его ещё нет: до первой задачи его не
	// существует, а группу нужно к чему-то привязать.
	err := s.rdb.XGroupCreateMkStream(ctx, streamKey, groupName, "0").Err()
	if err == nil || strings.Contains(err.Error(), "BUSYGROUP") {
		// BUSYGROUP — группа уже создана. Обычное дело при перезапуске сервиса.
		return nil
	}
	return fmt.Errorf("taskstore: ensure queue: %w", err)
}

// Enqueue кладёт номерок задачи в очередь.
func (s *Store) Enqueue(ctx context.Context, taskKey string) error {
	return s.enqueueAttempt(ctx, taskKey, 0)
}

// enqueueAttempt кладёт номерок с номером попытки. Общий путь для первой
// постановки и для возврата брошенной задачи уборщиком.
func (s *Store) enqueueAttempt(ctx context.Context, taskKey string, attempt int) error {
	err := s.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		MaxLen: streamMaxLen,
		Approx: true,
		Values: map[string]any{
			"key":        taskKey,
			attemptField: strconv.Itoa(attempt),
		},
	}).Err()
	if err != nil {
		return fmt.Errorf("taskstore: enqueue: %w", err)
	}
	return nil
}

// Dequeue ждёт и забирает задачу из очереди.
//
// Задача остаётся числиться за этим процессом до Ack. Блокируется не дольше
// blockTimeout; если за это время ничего не пришло — возвращает ErrNoTask.
func (s *Store) Dequeue(ctx context.Context) (Claim, error) {
	res, err := s.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    groupName,
		Consumer: s.consumer,
		Streams:  []string{streamKey, ">"}, // ">" = только то, что ещё никому не выдавали
		Count:    1,
		Block:    blockTimeout,
	}).Result()

	if errors.Is(err, redis.Nil) {
		return Claim{}, ErrNoTask
	}
	if err != nil {
		return Claim{}, fmt.Errorf("taskstore: dequeue: %w", err)
	}
	if len(res) == 0 || len(res[0].Messages) == 0 {
		return Claim{}, ErrNoTask
	}

	msg := res[0].Messages[0]
	key, ok := msg.Values["key"].(string)
	if !ok {
		// Запись есть, а номерка в ней нет — читать нечего. Расписываемся,
		// чтобы она не висела в выданных вечно и не мешала уборщику.
		_ = s.rdb.XAck(ctx, streamKey, groupName, msg.ID).Err()
		return Claim{}, fmt.Errorf("taskstore: dequeue: entry %s has no task key", msg.ID)
	}
	return Claim{TaskKey: key, ID: msg.ID}, nil
}

// Ack — расписка «задача доведена до конца, возвращать её не надо».
//
// Зовётся ТОЛЬКО после того, как результат записан. До этого момента задача
// обязана числиться выданной: в этом весь смысл замены списка на поток.
func (s *Store) Ack(ctx context.Context, c Claim) error {
	if err := s.rdb.XAck(ctx, streamKey, groupName, c.ID).Err(); err != nil {
		return fmt.Errorf("taskstore: ack: %w", err)
	}
	return nil
}

// attemptField — номер попытки, с которой едет задача.
//
// Счёт ведём в самой записи, а не полагаемся на счётчик Redis: возвращая
// задачу в очередь, мы кладём НОВУЮ запись, и счётчик выдач у неё начался бы
// с нуля. Тогда ядовитая задача ходила бы по кругу вечно — ровно то, от чего
// предел попыток и заводится.
const attemptField = "attempt"

// Reclaim возвращает в работу задачи, брошенные умершими процессами.
//
// Брошенной считается выданная и не подтверждённая дольше minIdle. Порог
// обязан быть заведомо больше времени обработки, иначе уборщик отберёт задачу
// у живого воркера и она посчитается дважды.
//
// Задачу, которую брали maxAttempts раз и ни разу не довели до конца, в
// очередь НЕ возвращаем: она роняет обработку сама по себе, и гонять её по
// кругу — это крутить сервис в перезапусках без всякого объяснения. Такие
// возвращаются вызывающему: пометить упавшими и объяснить причину — его дело,
// хранилище про смысл задач не знает.
//
// Возвращает: номерки, ушедшие на новый круг, и номерки признанных ядовитыми.
func (s *Store) Reclaim(ctx context.Context, minIdle time.Duration, maxAttempts int) (requeued, poisoned []string, err error) {
	// XAUTOCLAIM отдаёт записи вместе со значениями и заодно переписывает их
	// на нас — иначе они остались бы числиться за мёртвым процессом.
	msgs, _, err := s.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   streamKey,
		Group:    groupName,
		Consumer: s.consumer,
		MinIdle:  minIdle,
		Start:    "0",
		Count:    reclaimBatch,
	}).Result()
	if err != nil {
		return nil, nil, fmt.Errorf("taskstore: reclaim: %w", err)
	}

	for _, m := range msgs {
		key, ok := m.Values["key"].(string)
		if !ok {
			// Номерка в записи нет — возвращать нечего. Снимаем, чтобы не
			// висела вечно и не попадалась уборщику каждый раз.
			_ = s.rdb.XAck(ctx, streamKey, groupName, m.ID).Err()
			continue
		}

		attempt := attemptOf(m.Values) + 1
		if attempt >= maxAttempts {
			poisoned = append(poisoned, key)
			_ = s.rdb.XAck(ctx, streamKey, groupName, m.ID).Err()
			continue
		}

		// Новая запись, а не повторная выдача старой: обычные воркеры читают
		// только то, чего ещё никому не давали.
		if err := s.enqueueAttempt(ctx, key, attempt); err != nil {
			return requeued, poisoned, err
		}
		if err := s.rdb.XAck(ctx, streamKey, groupName, m.ID).Err(); err != nil {
			return requeued, poisoned, fmt.Errorf("taskstore: reclaim ack: %w", err)
		}
		requeued = append(requeued, key)
	}
	return requeued, poisoned, nil
}

// attemptOf — какая это была попытка. Нет поля — значит первая.
func attemptOf(values map[string]any) int {
	raw, ok := values[attemptField].(string)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return n
}

// Claimed — сколько задач выдано и не подтверждено прямо сейчас.
//
// Это и есть «взяли в работу, но не довели до конца». В покое ноль; растёт
// при обработке и обязано возвращаться к нулю. Постоянно ненулевое значение
// означает, что задачи теряются, — по нему это и видно снаружи.
func (s *Store) Claimed(ctx context.Context) (int64, error) {
	res, err := s.rdb.XPending(ctx, streamKey, groupName).Result()
	if err != nil {
		return 0, fmt.Errorf("taskstore: pending: %w", err)
	}
	return res.Count, nil
}
