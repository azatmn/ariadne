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

var ErrDecompressedTooLarge = errors.New("codec: decompressed data too large")

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

func Decode(routeCompressed string, maxDecompressedBytes int64) ([]geo.Point, error) {
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

	lr := io.LimitReader(zr, maxDecompressedBytes+1)
	jsonData, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("codec: zlib read: %w", err)
	}
	if int64(len(jsonData)) > maxDecompressedBytes {
		return nil, ErrDecompressedTooLarge
	}

	var wire []wirePoint
	if err = json.Unmarshal(jsonData, &wire); err != nil {
		return nil, fmt.Errorf("codec: json unmarshal: %w", err)
	}

	points := make([]geo.Point, len(wire))
	for i, w := range wire {
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

		points[i] = geo.Point{
			Time: t,
			Lon:  lon,
			Lat:  lat,
		}
	}

	return points, nil
}

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
