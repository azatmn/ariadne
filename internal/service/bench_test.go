package service

import (
	"context"
	"testing"
)

func BenchmarkResolve100(b *testing.B) {
	svc := New(testConfig())
	points := testPoints(100)
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		_, _ = svc.Resolve(ctx, points)
	}
}

func BenchmarkResolve3000(b *testing.B) {
	svc := New(testConfig())
	points := testPoints(3000)
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		_, _ = svc.Resolve(ctx, points)
	}
}
