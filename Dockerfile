FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o polymarket ./cmd/polymarket
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o sync ./cmd/sync

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/polymarket .
COPY --from=builder /app/api .
COPY --from=builder /app/sync .
# Default entrypoint: API service (Cloud Run Service).
# Cloud Run Jobs override this:
#   weather-polymarket: /app/polymarket --all-cities --yesterday
#   weather-sync:       /app/sync
ENTRYPOINT ["./api"]
