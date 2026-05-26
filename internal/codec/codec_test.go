package codec

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
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

	decoded, err := Decode(encoded, 100<<20)
	require.NoError(t, err, "Decode")

	require.Len(t, decoded, len(original))

	for i := range original {
		assert.True(t, original[i].Time.Equal(decoded[i].Time), "point %d: time %v != %v", i, original[i].Time, decoded[i].Time)
		assert.Equal(t, original[i].Lon, decoded[i].Lon, "point %d: lon", i)
		assert.Equal(t, original[i].Lat, decoded[i].Lat, "point %d: lat", i)
	}
}

func TestDecodeInvalidBase64(t *testing.T) {
	_, err := Decode("not-valid-base64!!!", 100<<20)
	require.Error(t, err, "expected error for invalid base64")
}

func TestDecodeInvalidZlib(t *testing.T) {
	// валидный base64, но внутри мусор — не zlib
	_, err := Decode("aGVsbG8gd29ybGQ=", 100<<20)
	require.Error(t, err, "expected error for invalid zlib")
}

func TestDecodeDecompressedTooLarge(t *testing.T) {
	points := []geo.Point{
		{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Lon: 37.0, Lat: 55.0},
		{Time: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC), Lon: 37.1, Lat: 55.1},
	}
	encoded, err := Encode(points)
	require.NoError(t, err, "Encode")

	_, err = Decode(encoded, 10)
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
		_, _ = Decode(input, 1<<20)
	})
}

func TestDecodeCoordinatesOutOfRange(t *testing.T) {
	raw := `[{"t":"2026-01-01T00:00:00Z","pos":{"x":200,"y":55}},{"t":"2026-01-01T00:00:01Z","pos":{"x":37,"y":55}}]`
	encoded := encodeRawJSON(t, raw)

	_, err := Decode(encoded, 100<<20)
	require.Error(t, err, "expected error for coordinates out of range")
	assert.Contains(t, err.Error(), "out of range")
}

func TestDecodeLatitudeOutOfRange(t *testing.T) {
	raw := `[{"t":"2026-01-01T00:00:00Z","pos":{"x":37,"y":-100}},{"t":"2026-01-01T00:00:01Z","pos":{"x":37,"y":55}}]`
	encoded := encodeRawJSON(t, raw)

	_, err := Decode(encoded, 100<<20)
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

	_, err := Decode(encoded, 100<<20)
	require.Error(t, err, "expected error for invalid JSON inside zlib")
	assert.Contains(t, err.Error(), "json unmarshal")
}

func TestDecodeInvalidTimeFormat(t *testing.T) {
	raw := `[{"t":"2026-13-45","pos":{"x":37,"y":55}},{"t":"2026-01-01T00:00:01Z","pos":{"x":37.1,"y":55.1}}]`
	encoded := encodeRawJSON(t, raw)

	_, err := Decode(encoded, 100<<20)
	require.Error(t, err, "expected error for invalid time format")
	assert.Contains(t, err.Error(), "parse time")
}

func TestEncodeEmpty(t *testing.T) {
	encoded, err := Encode([]geo.Point{})
	require.NoError(t, err, "Encode empty")

	decoded, err := Decode(encoded, 100<<20)
	require.NoError(t, err, "Decode empty")

	assert.Len(t, decoded, 0)
}
