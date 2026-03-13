# Docker build context должен быть корнем модуля memwatcher (../../ от этого файла).
# Makefile передаёт правильный контекст: docker build -f examples/oom-demo/Dockerfile .
#
# Структура в /src:
#   /src/                    ← корень memwatcher (go.mod module github.com/billz-2/memwatcher)
#   /src/examples/oom-demo/  ← этот пример (go.mod с replace ../..)

FROM golang:1.21-alpine AS builder

WORKDIR /src

# Копируем memwatcher модуль
COPY go.mod go.sum ./
COPY *.go ./
COPY templates/ ./templates/

# Копируем пример
COPY examples/oom-demo/ ./examples/oom-demo/

# Собираем из директории примера
WORKDIR /src/examples/oom-demo
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o /oom-demo .

FROM alpine:3.19

RUN apk add --no-cache ca-certificates

COPY --from=builder /oom-demo /oom-demo

EXPOSE 8080

ENTRYPOINT ["/oom-demo"]
