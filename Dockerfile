FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o polymarket ./cmd/polymarket
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o api ./cmd/api

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/polymarket .
COPY --from=builder /app/api .
# Default entrypoint: API service (Cloud Run Service).
# The Cloud Run Job overrides this with: /app/polymarket
ENTRYPOINT ["./api"]
