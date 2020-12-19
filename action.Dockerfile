FROM golang:1.15-alpine AS builder

WORKDIR /pullquote

COPY . ./

RUN CGO_ENABLED=0 go build ./...

FROM alpine

COPY --from=builder /pullquote/pullquote /usr/local/bin/pullquote
COPY scripts/action-entrypoint.sh /usr/local/bin/entrypoint.sh

WORKDIR /src

ENTRYPOINT ["entrypoint.sh"]
