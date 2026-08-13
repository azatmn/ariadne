# syntax=docker/dockerfile:1

# ---------- build ----------
FROM golang:1.26.5-alpine AS build

WORKDIR /src

# Кэш зависимостей
COPY go.mod go.sum* ./
RUN go mod download

# Исходники
COPY . .

# Статическая сборка под минимальный образ
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---------- runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/server /server

USER nonroot:nonroot
EXPOSE 8080 9090

ENTRYPOINT ["/server"]
