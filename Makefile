.PHONY: build run test lint docker-build docker-run docker-stop clean

APP_NAME := ariadne
TAG      := dev
PORT     := 8080

build:
	go build -o bin/server ./cmd/server

run:
	go run ./cmd/server

test:
	go test ./...

lint:
	go vet ./...

docker-build:
	docker build -t $(APP_NAME):$(TAG) .

docker-run:
	-docker stop $(APP_NAME) 2>/dev/null
	-docker rm $(APP_NAME) 2>/dev/null
	docker run -d --name $(APP_NAME) -p $(PORT):$(PORT) $(APP_NAME):$(TAG)

docker-stop:
	docker stop $(APP_NAME)

clean:
	rm -rf bin/
	-docker rmi $(APP_NAME):$(TAG) 2>/dev/null
