# RTSP/1.0 Proxy

High-performance RTSP/RTP proxy for IP cameras, designed to save bandwidth and reduce load on remote devices by fanning out a single upstream connection to multiple downstream clients.

## Synopsis

`rtsp://127.0.0.1:8554/rtsp/[login:password@]host[:port]/path`

Where:
- `127.0.0.1:8554`: RTSP proxy host and port.
- `/rtsp/`: Proxy path prefix.
- `login:password`: Credentials for the remote IP camera.
- `host`: IP/hostname of the target camera.
- `port`: Remote RTSP port (default: 554).
- `/path`: Camera stream path (e.g., `/Streaming/Channels/101`).

## Usage

```bash
./rtsp-proxy \
  -port 8554 \
  -log /var/log/rtsp-proxy.log \
  -verbose \
  -idle-timeout 20s \
  -packet-queue-size 1000 \
  -buffer-size 65536 \
  -dial-timeout 5s \
  -metrics-port 9090
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-port` | `554` | Port the proxy listens on |
| `-log` | `-` (stderr) | Log file path |
| `-verbose` | `false` | Detailed RTSP / state logging |
| `-idle-timeout` | `20s` | Disconnect idle upstream after this duration |
| `-packet-queue-size` | `1000` | Per-client RTP packet queue depth |
| `-buffer-size` | `65536` | Read buffer size (bytes) |
| `-dial-timeout` | `5s` | Upstream TCP dial timeout |
| `-metrics-port` | `0` (off) | HTTP port for Prometheus `/metrics` |

## Features

- **Connect On-Demand**: Establishes a connection to the camera only when needed. Metadata is fetched and cached automatically.
- **Multi-Client Fanout**: Efficiently shares a single upstream connection across multiple downstream clients using exactly one upstream session per unique URL/Credential set.
- **Automatic Reconnect**: Resilient reconnection logic with exponential backoff and context-aware cancellation.
- **Idle Disconnect**: Conserves resources by automatically closing idle upstream connections after a configurable timeout.
- **Slow Client Isolation**: Independent client queues prevent slow or stalled clients from impacting others or the upstream reader.
- **Digest qop=auth**: Full RFC 2617 Digest authentication including `qop=auth`, `nc`, `cnonce`, `opaque`.
- **Prometheus Metrics**: Optional `/metrics` endpoint (no external dependencies).
- **Thread-Safe Architecture**: Hardened for high concurrency using Go's synchronization primitives.
- **Optimized Networking**: `sync.Pool` for buffer management; client snapshot under lock for low-contention fan-out.

## Architecture

- **StreamManager**: Centralized registry ensuring stream uniqueness (keyed by `user:pass@host/path`).
- **State Machine**: `Disconnected` → `Connecting` → `Playing` ↔ `Reconnecting` → `Stopping`/`Destroyed`.
- **Single Stream object**: Remote is bound 1:1 to the StreamManager Stream — no duplicated internal Stream maps.
- **Fanout Model**: Single upstream reader dispatches packets to per-client buffered queues.
- **Shared RTSP parser**: Common line/header helpers in `message.go` used by Request and Response.

## Protocol Support

- RTSP/1.0
- RTP over TCP (Interleaved)
- Digest (with qop=auth) and Basic Authentication
- SDP Rewriting (IP translation for proxy transparency)
- Absolute and relative `a=control:` track URLs
- RTP-Info Rewriting

## Metrics

When `-metrics-port` is set, scrape `http://host:port/metrics`:

- `rtsp_proxy_packets_forwarded_total`
- `rtsp_proxy_packets_dropped_total`
- `rtsp_proxy_bytes_forwarded_total`
- `rtsp_proxy_reconnects_total`
- `rtsp_proxy_active_streams` / `rtsp_proxy_active_clients`
- `rtsp_proxy_auth_failures_total` / `rtsp_proxy_connect_errors_total`
- `rtsp_proxy_uptime_seconds`

## TODO

- Support for UDP transport.
- Support for Multicast upstream.
