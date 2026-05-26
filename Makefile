.PHONY: run test lint proto swagger bench fuzz

run:
	go run ./cmd/server

test:
	go test ./...

lint:
	go vet ./...

proto:
	protoc --go_out=. --go_opt=module=ariadne \
		--go-grpc_out=. --go-grpc_opt=module=ariadne \
		--proto_path=proto proto/route.proto

swagger:
	swag init -g cmd/server/main.go -o swagger/ --outputTypes go,json

bench:
	go test -bench=. -benchmem ./internal/service/ 2>&1 | grep -E "^(Benchmark|ok)"

fuzz:
	go test -fuzz=FuzzDecode -fuzztime=30s ./internal/codec/
