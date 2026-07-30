package rtspproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics holds process-wide counters exposed in Prometheus text format.
// No external dependencies — pure stdlib.
type Metrics struct {
	PacketsForwarded atomic.Uint64
	PacketsDropped   atomic.Uint64
	BytesForwarded   atomic.Uint64
	Reconnects       atomic.Uint64
	ActiveStreams    atomic.Int64
	ActiveClients    atomic.Int64
	AuthFailures     atomic.Uint64
	ConnectErrors    atomic.Uint64
	startTime        time.Time
}

// GlobalMetrics is the singleton metrics registry.
var GlobalMetrics = &Metrics{startTime: time.Now()}

// Handler returns an http.Handler that serves Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		uptime := time.Since(m.startTime).Seconds()

		fmt.Fprintf(w, "# HELP rtsp_proxy_uptime_seconds Time since process start.\n")
		fmt.Fprintf(w, "# TYPE rtsp_proxy_uptime_seconds gauge\n")
		fmt.Fprintf(w, "rtsp_proxy_uptime_seconds %.1f\n", uptime)

		fmt.Fprintf(w, "# HELP rtsp_proxy_packets_forwarded_total RTP packets forwarded to clients.\n")
		fmt.Fprintf(w, "# TYPE rtsp_proxy_packets_forwarded_total counter\n")
		fmt.Fprintf(w, "rtsp_proxy_packets_forwarded_total %d\n", m.PacketsForwarded.Load())

		fmt.Fprintf(w, "# HELP rtsp_proxy_packets_dropped_total RTP packets dropped (slow clients).\n")
		fmt.Fprintf(w, "# TYPE rtsp_proxy_packets_dropped_total counter\n")
		fmt.Fprintf(w, "rtsp_proxy_packets_dropped_total %d\n", m.PacketsDropped.Load())

		fmt.Fprintf(w, "# HELP rtsp_proxy_bytes_forwarded_total Bytes of RTP data forwarded.\n")
		fmt.Fprintf(w, "# TYPE rtsp_proxy_bytes_forwarded_total counter\n")
		fmt.Fprintf(w, "rtsp_proxy_bytes_forwarded_total %d\n", m.BytesForwarded.Load())

		fmt.Fprintf(w, "# HELP rtsp_proxy_reconnects_total Upstream reconnect attempts.\n")
		fmt.Fprintf(w, "# TYPE rtsp_proxy_reconnects_total counter\n")
		fmt.Fprintf(w, "rtsp_proxy_reconnects_total %d\n", m.Reconnects.Load())

		fmt.Fprintf(w, "# HELP rtsp_proxy_active_streams Currently active streams.\n")
		fmt.Fprintf(w, "# TYPE rtsp_proxy_active_streams gauge\n")
		fmt.Fprintf(w, "rtsp_proxy_active_streams %d\n", m.ActiveStreams.Load())

		fmt.Fprintf(w, "# HELP rtsp_proxy_active_clients Currently connected clients.\n")
		fmt.Fprintf(w, "# TYPE rtsp_proxy_active_clients gauge\n")
		fmt.Fprintf(w, "rtsp_proxy_active_clients %d\n", m.ActiveClients.Load())

		fmt.Fprintf(w, "# HELP rtsp_proxy_auth_failures_total Authentication failures.\n")
		fmt.Fprintf(w, "# TYPE rtsp_proxy_auth_failures_total counter\n")
		fmt.Fprintf(w, "rtsp_proxy_auth_failures_total %d\n", m.AuthFailures.Load())

		fmt.Fprintf(w, "# HELP rtsp_proxy_connect_errors_total Upstream connect/dial errors.\n")
		fmt.Fprintf(w, "# TYPE rtsp_proxy_connect_errors_total counter\n")
		fmt.Fprintf(w, "rtsp_proxy_connect_errors_total %d\n", m.ConnectErrors.Load())
	})
}

var (
	metricsServer   *http.Server
	metricsMu       sync.Mutex
	metricsListener net.Listener
)

// StartMetricsServer starts an HTTP server on the given port serving /metrics.
// Returns nil if port <= 0 (disabled). Safe to call once at startup.
func StartMetricsServer(port int) error {
	if port <= 0 {
		return nil
	}
	metricsMu.Lock()
	defer metricsMu.Unlock()

	if metricsServer != nil {
		return nil // already running
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", GlobalMetrics.Handler())
	addr := fmt.Sprintf(":%d", port)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("metrics listen %s: %w", addr, err)
	}
	metricsListener = ln
	metricsServer = &http.Server{Handler: mux}

	LogCriticalf("Metrics endpoint listening on %s/metrics", addr)
	go func() {
		if err := metricsServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			LogCriticalf("Metrics server error: %v", err)
		}
	}()
	return nil
}

// ShutdownMetricsServer gracefully stops the metrics HTTP server.
func ShutdownMetricsServer(ctx context.Context) error {
	metricsMu.Lock()
	srv := metricsServer
	metricsServer = nil
	metricsMu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}
