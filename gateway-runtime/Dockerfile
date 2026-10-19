# --- Tahap 1: Build ---
FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o gateway-app ./main.go

# --- Tahap 2: Run ---
FROM alpine:latest

# Tambahkan ca-certificates untuk koneksi TLS ke Fabric
RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /app/gateway-app .
COPY --from=builder /app/scripts ./scripts
# PENTING: Jangan COPY .env ke image!
# Secret diinjeksikan via --env-file atau Docker secrets saat runtime:
#   docker run --env-file .env auditchain-api
# Atau via docker-compose env_file directive (sudah ada di docker-compose.yml)

EXPOSE 8080

CMD ["./gateway-app"]