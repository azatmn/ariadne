.PHONY: run test lint vuln ci proto swagger bench fuzz

run:
	go run ./cmd/server

test:
	go test ./...

lint:
	go vet ./...

# Уязвимости — то же, что гоняет CI. Ставит инструмент, если его нет.
vuln:
	@command -v govulncheck >/dev/null || go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

# То же самое, что стадия `test` в .gitlab-ci.yml, и в том же порядке.
# Гонять перед пушем: дешевле подождать пять минут здесь, чем узнавать
# о падении из красной трубы.
#
# Одно отличие от CI остаётся и убрать его нельзя: там сборка идёт в образе
# с версией из `GO_VERSION`, здесь — тем компилятором, что стоит на машине.
# Разойтись они могут только если забыть обновить одно из двух мест, а этого
# как раз и не даёт строка `go` в go.mod — она точная, до заплатки.
ci: lint
	CGO_ENABLED=1 go test -race ./...
	$(MAKE) vuln

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
