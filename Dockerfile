FROM golang:1.23-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/bot   ./cmd/bot   && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/admin ./cmd/admin

# ── bot ──────────────────────────────────────────────────────────────────────
FROM alpine:3.20 AS bot

RUN adduser -D -u 10001 appuser
WORKDIR /app
COPY --from=builder /out/bot /app/bot
USER appuser
ENTRYPOINT ["/app/bot"]

# ── admin ────────────────────────────────────────────────────────────────────
FROM alpine:3.20 AS admin

RUN adduser -D -u 10001 appuser
WORKDIR /app
COPY --from=builder /out/admin /app/admin
USER appuser
ENTRYPOINT ["/app/admin"]
