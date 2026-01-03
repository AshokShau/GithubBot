FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o bot cmd/bot/main.go

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/bot .

RUN apk --no-cache add ca-certificates

CMD ["./bot"]
