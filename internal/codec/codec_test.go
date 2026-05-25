package codec

import (
	"errors"
	"testing"
	"time"

	"ariadne/internal/geo"
)

func TestDecodeEncode(t *testing.T) {
	original := []geo.Point{
		{Time: time.Date(2026, 3, 16, 10, 12, 20, 0, time.FixedZone("", 3*3600)), Lon: 38.082661, Lat: 54.015431},
		{Time: time.Date(2026, 3, 16, 10, 12, 25, 0, time.FixedZone("", 3*3600)), Lon: 38.082700, Lat: 54.015500},
		{Time: time.Date(2026, 3, 16, 10, 12, 30, 0, time.FixedZone("", 3*3600)), Lon: 38.082800, Lat: 54.015600},
	}

	encoded, err := Encode(original)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	decoded, err := Decode(encoded, 100<<20)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if len(decoded) != len(original) {
		t.Fatalf("expected %d points, got %d", len(original), len(decoded))
	}

	for i := range original {
		if !original[i].Time.Equal(decoded[i].Time) {
			t.Errorf("point %d: time %v != %v", i, original[i].Time, decoded[i].Time)
		}
		if original[i].Lon != decoded[i].Lon {
			t.Errorf("point %d: lon %f != %f", i, original[i].Lon, decoded[i].Lon)
		}
		if original[i].Lat != decoded[i].Lat {
			t.Errorf("point %d: lat %f != %f", i, original[i].Lat, decoded[i].Lat)
		}
	}
}

func TestDecodeInvalidBase64(t *testing.T) {
	_, err := Decode("not-valid-base64!!!", 100<<20)
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestDecodeInvalidZlib(t *testing.T) {
	// валидный base64, но внутри мусор — не zlib
	_, err := Decode("aGVsbG8gd29ybGQ=", 100<<20)
	if err == nil {
		t.Fatal("expected error for invalid zlib")
	}
}

func TestDecodeDecompressedTooLarge(t *testing.T) {
	points := []geo.Point{
		{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Lon: 37.0, Lat: 55.0},
		{Time: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC), Lon: 37.1, Lat: 55.1},
	}
	encoded, err := Encode(points)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	_, err = Decode(encoded, 10)
	if err == nil {
		t.Fatal("expected error for decompressed data too large")
	}
	if !errors.Is(err, ErrDecompressedTooLarge) {
		t.Errorf("expected ErrDecompressedTooLarge, got: %v", err)
	}
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
		Decode(input, 1<<20)
	})
}

func TestEncodeEmpty(t *testing.T) {
	encoded, err := Encode([]geo.Point{})
	if err != nil {
		t.Fatalf("Encode empty: %v", err)
	}

	decoded, err := Decode(encoded, 100<<20)
	if err != nil {
		t.Fatalf("Decode empty: %v", err)
	}

	if len(decoded) != 0 {
		t.Errorf("expected 0 points, got %d", len(decoded))
	}
}
