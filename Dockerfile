FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/migrate ./cmd/migrate

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates && \
    addgroup -S -g 10001 archive && adduser -S -D -H -u 10001 -G archive archive && \
    mkdir -p /srv/data/files && chown -R archive:archive /srv/data
COPY --from=build /out/server /app/server
COPY --from=build /out/worker /app/worker
COPY --from=build /out/migrate /app/migrate
USER archive
WORKDIR /app
CMD ["/app/server"]

FROM runtime AS ocr-worker
USER root
RUN apk add --no-cache poppler-utils tesseract-ocr tesseract-ocr-data-chi_sim tesseract-ocr-data-eng
USER archive
CMD ["/app/worker"]

FROM runtime AS api
