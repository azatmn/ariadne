package codec

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"ariadne/internal/geo"
)

func TestDecodeEncode(t *testing.T) {
	original := []geo.Point{
		{Time: time.Date(2026, 3, 16, 10, 12, 20, 0, time.FixedZone("", 3*3600)), Lon: 38.082661, Lat: 54.015431},
		{Time: time.Date(2026, 3, 16, 10, 12, 25, 0, time.FixedZone("", 3*3600)), Lon: 38.082700, Lat: 54.015500},
		{Time: time.Date(2026, 3, 16, 10, 12, 30, 0, time.FixedZone("", 3*3600)), Lon: 38.082800, Lat: 54.015600},
	}

	encoded, err := Encode(original)
	require.NoError(t, err, "Encode")

	decoded, err := Decode(encoded, Limits{DecompressedBytes: 100 << 20, Points: testMaxPoints})
	require.NoError(t, err, "Decode")

	require.Len(t, decoded, len(original))

	for i := range original {
		assert.True(t, original[i].Time.Equal(decoded[i].Time), "point %d: time %v != %v", i, original[i].Time, decoded[i].Time)
		assert.Equal(t, original[i].Lon, decoded[i].Lon, "point %d: lon", i)
		assert.Equal(t, original[i].Lat, decoded[i].Lat, "point %d: lat", i)
	}
}

func TestDecodeInvalidBase64(t *testing.T) {
	_, err := Decode("not-valid-base64!!!", Limits{DecompressedBytes: 100 << 20, Points: testMaxPoints})
	require.Error(t, err, "expected error for invalid base64")
}

func TestDecodeInvalidZlib(t *testing.T) {
	// валидный base64, но внутри мусор — не zlib
	_, err := Decode("aGVsbG8gd29ybGQ=", Limits{DecompressedBytes: 100 << 20, Points: testMaxPoints})
	require.Error(t, err, "expected error for invalid zlib")
}

func TestDecodeDecompressedTooLarge(t *testing.T) {
	points := []geo.Point{
		{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Lon: 37.0, Lat: 55.0},
		{Time: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC), Lon: 37.1, Lat: 55.1},
	}
	encoded, err := Encode(points)
	require.NoError(t, err, "Encode")

	_, err = Decode(encoded, Limits{DecompressedBytes: 10, Points: testMaxPoints})
	require.ErrorIs(t, err, ErrDecompressedTooLarge)
}

func TestDecodeZlibBomb(t *testing.T) {
	// 50 000 одинаковых точек — zlib сжимает повторы очень хорошо.
	// Сжатый размер ~нескольких КБ, а после распаковки ~4.5 MB JSON.
	// Лимит ставим 1 MB — должен сработать ErrDecompressedTooLarge.
	var points []wirePoint
	for i := range 50_000 {
		points = append(points, wirePoint{
			T:   time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339),
			Pos: wirePos{X: 37.0, Y: 55.0},
		})
	}

	jsonData, err := json.Marshal(points)
	require.NoError(t, err)

	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	require.NoError(t, err)
	_, err = w.Write(jsonData)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())

	// Сжатый размер гораздо меньше распакованного
	assert.Less(t, len(buf.Bytes()), len(jsonData)/10, "compression ratio should be > 10x")

	_, err = Decode(encoded, Limits{DecompressedBytes: 1 << 20, Points: testMaxPoints}) // лимит 1 MB
	require.ErrorIs(t, err, ErrDecompressedTooLarge)
}

func FuzzDecode(f *testing.F) {
	f.Add("")
	f.Add("not-valid-base64!!!")
	f.Add("aGVsbG8gd29ybGQ=")

	points := []geo.Point{
		{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Lon: 37.0, Lat: 55.0},
		{Time: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC), Lon: 37.1, Lat: 55.1},
	}
	if encoded, err := Encode(points); err == nil {
		f.Add(encoded)
	}

	f.Fuzz(func(t *testing.T, input string) {
		_, _ = Decode(input, Limits{DecompressedBytes: 1 << 20, Points: testMaxPoints})
	})
}

func TestDecodeCoordinatesOutOfRange(t *testing.T) {
	raw := `[{"t":"2026-01-01T00:00:00Z","pos":{"x":200,"y":55}},{"t":"2026-01-01T00:00:01Z","pos":{"x":37,"y":55}}]`
	encoded := encodeRawJSON(t, raw)

	_, err := Decode(encoded, Limits{DecompressedBytes: 100 << 20, Points: testMaxPoints})
	require.Error(t, err, "expected error for coordinates out of range")
	assert.Contains(t, err.Error(), "out of range")
}

func TestDecodeLatitudeOutOfRange(t *testing.T) {
	raw := `[{"t":"2026-01-01T00:00:00Z","pos":{"x":37,"y":-100}},{"t":"2026-01-01T00:00:01Z","pos":{"x":37,"y":55}}]`
	encoded := encodeRawJSON(t, raw)

	_, err := Decode(encoded, Limits{DecompressedBytes: 100 << 20, Points: testMaxPoints})
	require.Error(t, err, "expected error for latitude out of range")
}

func encodeRawJSON(t *testing.T, jsonStr string) string {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	require.NoError(t, err)
	_, err = zw.Write([]byte(jsonStr))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestDecodeInvalidJSONInsideZlib(t *testing.T) {
	raw := `{not json at all!!!}`
	encoded := encodeRawJSON(t, raw)

	_, err := Decode(encoded, Limits{DecompressedBytes: 100 << 20, Points: testMaxPoints})
	require.Error(t, err, "expected error for invalid JSON inside zlib")
	// Не массив — отдельная беда от «массив, но с мусором внутри»: разбор
	// теперь потоковый и открывающую скобку читает первым же шагом.
	assert.Contains(t, err.Error(), "ожидался массив точек")
}

// Массив без закрывающей скобки. При потоковом разборе это отдельный случай:
// элементы читаются один за другим и без явной проверки конца обрубленный
// вход прошёл бы за годный.
func TestDecodeUnclosedArray(t *testing.T) {
	raw := `[{"t":"2026-01-01T00:00:00Z","pos":{"x":37.0,"y":55.0}}`
	_, err := Decode(encodeRawJSON(t, raw), Limits{DecompressedBytes: 100 << 20, Points: testMaxPoints})
	require.Error(t, err, "обрубленный массив — не годный вход")
}

func TestDecodeInvalidTimeFormat(t *testing.T) {
	raw := `[{"t":"2026-13-45","pos":{"x":37,"y":55}},{"t":"2026-01-01T00:00:01Z","pos":{"x":37.1,"y":55.1}}]`
	encoded := encodeRawJSON(t, raw)

	_, err := Decode(encoded, Limits{DecompressedBytes: 100 << 20, Points: testMaxPoints})
	require.Error(t, err, "expected error for invalid time format")
	assert.Contains(t, err.Error(), "parse time")
}

func TestEncodeEmpty(t *testing.T) {
	encoded, err := Encode([]geo.Point{})
	require.NoError(t, err, "Encode empty")

	decoded, err := Decode(encoded, Limits{DecompressedBytes: 100 << 20, Points: testMaxPoints})
	require.NoError(t, err, "Decode empty")

	assert.Len(t, decoded, 0)
}

// --- потолок на число точек ---------------------------------------------

// testMaxPoints — боевое значение `MAX_POINTS`. Важно брать именно его:
// прежние два теста на размер ставили лимит НИЖЕ нагрузки (10 байт, 1 МБ) и
// потому проверяли только срабатывание предохранителя, а не поведение под
// настоящими лимитами.
const testMaxPoints = 50_000

// allocatedMB — сколько памяти запросила функция за один вызов.
func allocatedMB(t *testing.T, fn func()) float64 {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / (1 << 20)
}

// zip сжимает готовый JSON так, как это делает PHP: zlib + base64.
func zip(t *testing.T, jsonText string) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	require.NoError(t, err)
	_, err = w.Write([]byte(jsonText))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// Пустышки `{}` — самый дешёвый элемент массива: три байта вместе с запятой.
// Двадцать мегабайт таких дают семь миллионов элементов, а на проводе это 27 КБ,
// то есть байтовые лимиты пропускают их не моргнув.
//
// Раньше здесь уходило 1547 МБ при `mem_limit: 256m` — контейнер умирал от
// одного запроса. Разбор шёл целиком, а потолок на число точек проверялся
// далеко после, в `service.Resolve`.
func TestDecodeCheapElementsBombDoesNotAllocate(t *testing.T) {
	const n = 6_990_497 // ровно 20 МБ JSON
	body := "[" + strings.TrimSuffix(strings.Repeat("{},", n), ",") + "]"
	require.Greater(t, len(body), 20<<20-1024, "нагрузка должна упираться в байтовый лимит")

	payload := zip(t, body)
	require.Less(t, len(payload), 1<<20, "на проводе это должно быть меньше мегабайта")

	var err error
	mb := allocatedMB(t, func() {
		_, err = Decode(payload, Limits{DecompressedBytes: 20 << 20, Points: testMaxPoints})
	})

	require.Error(t, err, "пустышки — не точки, разбор обязан отказать")
	assert.Less(t, mb, 32.0, "разбор запросил %.0f МБ — потолок на точки не работает", mb)
}

// Валидные точки сверх потолка: их разбирать можно, но не нужно — отказ обязан
// прийти по счёту, а не после того, как всё уже разложено по памяти.
func TestDecodeRejectsTooManyPoints(t *testing.T) {
	var b strings.Builder
	b.WriteByte('[')
	for i := range testMaxPoints + 1000 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"t":"2026-01-01T00:00:%02dZ","pos":{"x":37.0,"y":55.0}}`, i%60)
	}
	b.WriteByte(']')

	payload := zip(t, b.String())

	var err error
	mb := allocatedMB(t, func() {
		_, err = Decode(payload, Limits{DecompressedBytes: 20 << 20, Points: testMaxPoints})
	})

	require.ErrorIs(t, err, ErrTooManyPoints)
	assert.Less(t, mb, 32.0, "разбор запросил %.0f МБ вместо того, чтобы остановиться на потолке", mb)
}

// Ровно потолок — это ещё годный маршрут, отказывать нельзя.
func TestDecodeAllowsExactlyMaxPoints(t *testing.T) {
	const n = 1000
	var b strings.Builder
	b.WriteByte('[')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"t":"2026-01-01T00:00:%02dZ","pos":{"x":37.0,"y":55.0}}`, i%60)
	}
	b.WriteByte(']')

	pts, err := Decode(zip(t, b.String()), Limits{DecompressedBytes: 20 << 20, Points: n})
	require.NoError(t, err, "ровно потолок — это годный вход")
	assert.Len(t, pts, n)
}

// Потолок обязан быть задан. Ноль или отрицательное — это не «без ограничений»,
// а испорченная настройка: тихо снять потолок значит вернуть ту самую бомбу.
func TestDecodeRequiresPositiveMaxPoints(t *testing.T) {
	payload := zip(t, `[{"t":"2026-01-01T00:00:00Z","pos":{"x":37.0,"y":55.0}}]`)
	for _, max := range []int{0, -1} {
		_, err := Decode(payload, Limits{DecompressedBytes: 20 << 20, Points: max})
		require.Error(t, err, "maxPoints=%d обязан быть отказом", max)
	}
}
