.PHONY: build run test lint docker docker-run docker-stop

build:
	go build -o bin/server ./cmd/server

run:
	go run ./cmd/server

test:
	go test ./...

lint:
	go vet ./...

docker:
	docker build -t ariadne:dev .

docker-run:
	docker run -d --name ariadne -p 8080:8080 ariadne:dev

docker-stop:
	docker stop ariadne
