# syntax=docker/dockerfile:1

# ---------- Stage 1: Build ----------
FROM golang:1.25-alpine AS builder

# git kadang dibutuhkan oleh go mod download untuk dependency yang belum ada di cache
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Copy go.mod & go.sum dulu supaya layer cache dependency terpisah dari source code
COPY go.mod go.sum ./
RUN go mod download

# Copy seluruh source code
COPY . .

# go-ora adalah driver pure-Go (tanpa CGO/Oracle Instant Client), jadi build static aman
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /out/auditchain-agent .

# ---------- Stage 2: Runtime ----------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1000 agent

WORKDIR /app

COPY --from=builder /out/auditchain-agent /app/auditchain-agent
COPY config.yml /app/config.yml

# Durable idempotency state is outside the application image and writable by
# the non-root runtime user. The recovery policy is mounted read-only by
# Compose/deployment configuration.
RUN mkdir -p /var/lib/auditchain-agent && chown -R agent:agent /var/lib/auditchain-agent
VOLUME ["/var/lib/auditchain-agent"]

# Jalankan sebagai non-root
USER agent

# Port verify server (Lapis 3 AuditChain) — sesuaikan bila AGENT_VERIFY_PORT diubah
EXPOSE 9090

ENTRYPOINT ["/app/auditchain-agent"]
CMD ["config.yml"]
