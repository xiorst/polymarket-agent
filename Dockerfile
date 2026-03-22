FROM golang:1.25-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /agent ./cmd/agent

# Runtime
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /agent /app/agent
COPY config.yaml /app/config.yaml

EXPOSE 8080

ENTRYPOINT ["/app/agent"]
CMD ["--config", "/app/config.yaml", "--log-level", "info"]
