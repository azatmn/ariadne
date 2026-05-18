.PHONY: run test lint

run:
	go run ./cmd/server

test:
	go test ./...

lint:
	go vet ./...
