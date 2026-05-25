.PHONY: run test lint proto swagger

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
