FROM golang:1.26

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go generate
RUN go build -o bot .

CMD ["./bot"]
