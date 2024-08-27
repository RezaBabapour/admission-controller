FROM golang:1.23-alpine as dev

WORKDIR /app

FROM dev as build
COPY go.mod /go.sum /app/
RUN GOPROXY=https://goproxy.io,direct go mod download

COPY . /app/

RUN CGO_ENABLED=0 go build -o app

FROM alpine:3.20 as runtime

COPY --from=build /app/app /usr/local/bin/app
RUN chmod +x /usr/local/bin/app

ENTRYPOINT ["app"]
