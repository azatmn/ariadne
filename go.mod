module ariadne

// Версия ТОЧНАЯ, до заплатки, а не просто `1.26`. Причина не в аккуратности.
//
// В стандартной библиотеке go1.26.0 семнадцать уязвимостей, до которых наш код
// дотягивается: `crypto/tls` на исходящих запросах к OSRM и коллбэкам,
// `net/textproto` и `net` на разборе входящих. Все закрыты заплатками до 1.26.5.
//
// Почему именно ЭТА строка, а не `toolchain`. Строку `go` Go проверяет ВСЕГДА,
// в том числе когда качать новые версии запрещено — а запрещено оно в
// официальных образах golang (`GOTOOLCHAIN=local`). Отдельная строка
// `toolchain go1.26.5` там просто игнорируется: проверено, CI собрал и прогнал
// тесты на уязвимой 1.26.3, имея её в go.mod. С точной строкой `go` он
// откажется собирать вовсе:
//
//	go: go.mod requires go >= 1.26.5 (running go 1.26.0; GOTOOLCHAIN=local)
//
// Где качать разрешено (обычная машина разработчика), Go скачает нужную
// заплатку сам — ставить её руками не надо.
go 1.26.5

require (
	github.com/alicebob/miniredis/v2 v2.38.0
	github.com/go-chi/chi/v5 v5.2.5
	github.com/google/uuid v1.6.0
	github.com/redis/go-redis/v9 v9.21.0
	github.com/stretchr/testify v1.11.1
	github.com/swaggo/http-swagger/v2 v2.0.2
	github.com/swaggo/swag v1.16.6
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/KyleBanks/depth v1.2.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/go-openapi/jsonpointer v0.19.5 // indirect
	github.com/go-openapi/jsonreference v0.20.0 // indirect
	github.com/go-openapi/spec v0.20.6 // indirect
	github.com/go-openapi/swag v0.19.15 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/mailru/easyjson v0.7.6 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/swaggo/files/v2 v2.0.0 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/mod v0.34.0 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	golang.org/x/tools v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
