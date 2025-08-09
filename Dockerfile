FROM golang:1.22

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . ./

RUN find && CGO_ENABLED=0 GOOS=linux go build -o /ccma

EXPOSE 8080

CMD ["/ccma"]
