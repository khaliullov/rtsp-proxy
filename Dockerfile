# syntax=docker/dockerfile:1

# -----------------------------------------------------------------------------
# Build stage
# -----------------------------------------------------------------------------
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache ca-certificates git

WORKDIR /src

# Cache module downloads
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true

COPY . .

# Static binary, no CGO
RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/rtsp-proxy ./cmd/

# -----------------------------------------------------------------------------
# Runtime stage
# -----------------------------------------------------------------------------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 rtsp

WORKDIR /app

COPY --from=builder /out/rtsp-proxy /app/rtsp-proxy

# Default ports: RTSP proxy + optional metrics
EXPOSE 8554 9090

USER rtsp

# Defaults suitable for containers (non-privileged port)
ENV RTSP_PORT=8554 \
    METRICS_PORT=9090

ENTRYPOINT ["/app/rtsp-proxy"]
CMD ["-port", "8554", "-metrics-port", "9090", "-log", "-"]
