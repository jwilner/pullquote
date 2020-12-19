FROM golang:1.15-alpine AS builder

WORKDIR /pullquote

COPY . ./

RUN go build ./...

# we rely on the `go` binary being present
FROM golang:1.15-alpine

COPY --from=builder /pullquote/pullquote /usr/local/bin/pullquote

WORKDIR /src

ENTRYPOINT ["pullquote"]
