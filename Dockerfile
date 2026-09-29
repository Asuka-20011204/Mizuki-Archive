FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && \
    addgroup -S -g 10001 archive && adduser -S -D -H -u 10001 -G archive archive && \
    mkdir -p /srv/data/files && chown -R archive:archive /srv/data
COPY --from=build /out/server /app/server
COPY --from=build /out/worker /app/worker
USER archive
WORKDIR /app
CMD ["/app/server"]
