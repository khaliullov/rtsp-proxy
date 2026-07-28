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
./rtsp-proxy -port 8554 -log /var/log/rtsp-proxy.log -verbose
```

- `-port`: Port the proxy server will listen on (default: 554).
- `-log`: Log file path (default: `-` for stderr).
- `-verbose`: Enables detailed logging of RTSP traffic and internal state transitions.

## Features

- **Connect On-Demand**: Establishing a connection to the camera only when needed. Metadata is fetched and cached automatically.
- **Multi-Client Fanout**: Efficiently shares a single upstream connection across multiple downstream clients.
- **Automatic Reconnect**: Resilient reconnection logic with exponential backoff and context-aware cancellation.
- **Idle Disconnect**: Conserves resources by automatically closing idle upstream connections.
- **Slow Client Isolation**: Independent client queues prevent slow or stalled clients from impacting others or the upstream stream.
- **Thread-Safe Architecture**: Designed for high concurrency using Go's proven synchronization primitives.
- **Expanded Metrics**: Real-time tracking of bitrate, packet counts, and connection health.

## Architecture

- **StreamManager**: Centralized registry for stream lifecycle management.
- **State Machine**: Each stream follows a strictly enforced state lifecycle: `Disconnected` → `Connecting` -> `Playing` ↔ `Reconnecting`.
- **Fanout Model**: Upstream reader dispatches packets to per-client buffered queues, handled by dedicated writer goroutines.
- **Memory Management**: Optimized packet dispatching to minimize allocations and pressure on the garbage collector.

## Protocol Support

- RTSP/1.0
- RTP over TCP (Interleaved)
- Digest and Basic Authentication
- SDP Rewriting (IP translation for proxy transparency)
- RTP-Info Rewriting (Sequence and timestamp synchronization)

## TODO

- Support for UDP transport.
- Support for Multicast upstream.
- Prometheus metrics exporter.
