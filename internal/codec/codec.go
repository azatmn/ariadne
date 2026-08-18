// Package codec переводит маршрут между форматом провода и внутренним видом:
// base64 → zlib → JSON → []geo.Point и обратно.
//
// Формат задан PHP-бэкендом, и в нём заложена ловушка: gzcompress() в PHP даёт
// zlib (заголовок 0x78), а НЕ gzip. compress/gzip на таких данных отвечает
// «Not a gzipped file».
//
// Вход приходит снаружи и по определению враждебен, поэтому распаковка идёт
// под двумя потолками сразу — см. Limits.
package codec

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"ariadne/internal/geo"
)

var (
	// ErrDecompressedTooLarge — распакованные данные не влезли в потолок байт.
	// Отдаётся вместо EOF, чтобы отличить бомбу сжатия от битого входа.
	ErrDecompressedTooLarge = errors.New("codec: decompressed data too large")

	// ErrTooManyPoints — во входе больше точек, чем разрешено.
	//
	// Свой, а не `service.ErrTooManyPoints`: разбор не должен знать про сервис.
	// Оба отказа означают для клиента одно и то же и обязаны отдаваться одним
	// кодом ответа — см. места, где они разбираются.
	ErrTooManyPoints = errors.New("codec: too many points")
)

// Limits — потолки разбора. Оба нужны и оба защищают от разного:
// байты — от бомбы сжатия, точки — от дешёвых элементов вроде `{}`, которых
// в двадцать мегабайт влезает семь миллионов.
//
// Структурой, а не двумя числами в списке аргументов: рядом стоящие `int64` и
// `int` легко переставить местами, и компилятор такого не заметит — а сервис
// начнёт принимать семь миллионов точек в двадцати байтах.
type Limits struct {
	DecompressedBytes int64 // потолок на распакованный JSON
	Points            int   // потолок на число точек
}

// capReader читает не больше limit байт и объявляет об этом сам.
//
// `io.LimitReader` для этого не годится: он возвращает EOF, и разбор JSON
// сообщил бы про «неожиданный конец данных» — то есть про испорченный вход
// вместо превышенного лимита. Разница видна клиенту: в первом случае он чинит
// свои данные, во втором шлёт трек по частям.
type capReader struct {
	r    io.Reader
	left int64
}

// Read отдаёт данные, пока не исчерпан остаток; после этого — свою ошибку.
func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, ErrDecompressedTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// wirePoint — формат точки на проводе (JSON от PHP backend).
// PHP gzcompress() = zlib (заголовок 0x78), НЕ gzip.
type wirePoint struct {
	T   string  `json:"t"`
	Pos wirePos `json:"pos"`
}

type wirePos struct {
	X float64 `json:"x"` // longitude
	Y float64 `json:"y"` // latitude
}

// Decode разбирает сжатый маршрут в точки.
//
// Разбор ПОТОКОВЫЙ, и это не про скорость, а про память. Раньше сюда приходил
// весь JSON целиком, `json.Unmarshal` строил из него срез, и только потом,
// далеко отсюда — в `service.Resolve` — проверялось число точек. Байтовый лимит
// такого не ловит: самый дешёвый элемент массива это три символа `{},`, и
// двадцать мегабайт таких дают семь миллионов элементов. Замерено: запрос на
// 27 КБ просил 1547 МБ при `mem_limit: 256m`, то есть один запрос убивал
// контейнер.
//
// Теперь элементы читаются по одному, а счёт проверяется на каждом. Расход
// ограничен `Limits.Points` независимо от того, что прислали.
//
// `Limits.Points` обязан быть положительным: ноль — это не «без ограничений»,
// а испорченная настройка, и тихо снимать потолок нельзя.
func Decode(routeCompressed string, lim Limits) ([]geo.Point, error) {
	if lim.Points <= 0 {
		return nil, fmt.Errorf("codec: Limits.Points must be positive, got %d", lim.Points)
	}

	compressed, err := base64.StdEncoding.DecodeString(routeCompressed)
	if err != nil {
		return nil, fmt.Errorf("codec: base64 decode: %w", err)
	}

	zr, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("codec: zlib open: %w", err)
	}
	defer func() {
		_ = zr.Close()
	}()

	dec := json.NewDecoder(&capReader{r: zr, left: lim.DecompressedBytes})

	// Открывающая скобка. Отдельным шагом, чтобы «это вообще не массив»
	// отличалось от «массив, но с мусором внутри».
	tok, err := dec.Token()
	if err != nil {
		return nil, decodeErr(err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, fmt.Errorf("codec: expected an array of points, got %v", tok)
	}

	var points []geo.Point
	for dec.More() {
		if len(points) >= lim.Points {
			return nil, fmt.Errorf("%w: more than %d", ErrTooManyPoints, lim.Points)
		}

		var w wirePoint
		if err := dec.Decode(&w); err != nil {
			return nil, decodeErr(err)
		}

		i := len(points)
		t, err := time.Parse(time.RFC3339, w.T)
		if err != nil {
			return nil, fmt.Errorf("codec: parse time %q at index %d: %w", w.T, i, err)
		}
		lon, lat := w.Pos.X, w.Pos.Y
		if math.IsNaN(lon) || math.IsNaN(lat) || math.IsInf(lon, 0) || math.IsInf(lat, 0) {
			return nil, fmt.Errorf("codec: invalid coordinates at index %d", i)
		}
		if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			return nil, fmt.Errorf("codec: coordinates out of range at index %d: lon=%f lat=%f", i, lon, lat)
		}

		points = append(points, geo.Point{Time: t, Lon: lon, Lat: lat})
	}

	// Закрывающая скобка: без неё «[{...}» прошло бы за годный вход.
	if _, err := dec.Token(); err != nil {
		return nil, decodeErr(err)
	}

	// И ничего после неё. `json.Unmarshal` читал документ целиком и на хвост
	// ругался сам; потоковый разбор доходит до `]` и уходит, если не смотреть
	// дальше. Хвост означает, что за маршрут приняли что-то, чего клиент не
	// посылал: склеенные ответы, обрезанную дозапись, чужой кусок в теле.
	// Пробелы и перевод строки хвостом не считаются — их пропускает сам
	// декодер.
	// Проверяем именно КОНЕЦ ДАННЫХ, а не `dec.More()`: тот отвечает «есть ли
	// ещё элемент внутри массива», и на одиночную `]` в хвосте скажет «нет» —
	// она не значение.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("codec: unexpected data after the array of points")
	}

	return points, nil
}

// decodeErr сохраняет распознаваемые причины и заворачивает остальное.
//
// Разбор JSON читает через `capReader`, и превышение лимита приходит сюда как
// обычная ошибка чтения. Завернуть её в «json unmarshal» значило бы сказать
// клиенту «у вас битые данные» вместо «трек слишком большой» — а это разные
// действия с его стороны.
func decodeErr(err error) error {
	if errors.Is(err, ErrDecompressedTooLarge) {
		return ErrDecompressedTooLarge
	}
	return fmt.Errorf("codec: json unmarshal: %w", err)
}

// Encode собирает маршрут обратно в строку для ответа: JSON → zlib → base64.
//
// Время пишется в RFC3339 — в том же виде, в каком пришло. Сжатие на
// максимальном уровне: ответ уходит по сети один раз, а процессорного времени
// на нём тратится несравнимо меньше, чем в самой чистке.
func Encode(points []geo.Point) (string, error) {
	wire := make([]wirePoint, len(points))
	for i, p := range points {
		wire[i] = wirePoint{
			T:   p.Time.Format(time.RFC3339),
			Pos: wirePos{X: p.Lon, Y: p.Lat},
		}
	}

	jsonData, err := json.Marshal(wire)
	if err != nil {
		return "", fmt.Errorf("codec: json marshal: %w", err)
	}

	var buf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	if err != nil {
		return "", fmt.Errorf("codec: zlib writer: %w", err)
	}
	if _, err = zw.Write(jsonData); err != nil {
		return "", fmt.Errorf("codec: zlib write: %w", err)
	}
	if err = zw.Close(); err != nil {
		return "", fmt.Errorf("codec: zlib close: %w", err)
	}

	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
