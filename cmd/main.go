package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/khaliullov/rtsp-proxy/rtspproxy"
)

func main() {
	var logFile string
	var portNum int
	var verbose bool
	var idleTimeout time.Duration
	var packetQueueSize int
	var bufferSize int
	var dialTimeout time.Duration
	var metricsPort int

	flag.StringVar(&logFile, "log", "-", "log file")
	flag.IntVar(&portNum, "port", 554, "server port")
	flag.BoolVar(&verbose, "verbose", false, "enable verbose logging")
	flag.DurationVar(&idleTimeout, "idle-timeout", 20*time.Second, "idle upstream disconnect timeout")
	flag.IntVar(&packetQueueSize, "packet-queue-size", 1000, "per-client packet queue size")
	flag.IntVar(&bufferSize, "buffer-size", 65536, "RTP/RTSP read buffer size in bytes")
	flag.DurationVar(&dialTimeout, "dial-timeout", 5*time.Second, "upstream dial timeout")
	flag.IntVar(&metricsPort, "metrics-port", 0, "Prometheus metrics HTTP port (0=disabled)")
	flag.Parse()

	if logFile == "-" {
		log.SetOutput(os.Stderr)
	} else {
		f, err := os.OpenFile(logFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			log.Fatalf("error opening log file %s: %v", logFile, err)
		}
		defer f.Close()
		log.SetOutput(f)
	}

	// Apply CLI overrides to GlobalConfig before any Stream/Client is created
	cfg := rtspproxy.GlobalConfig
	cfg.IdleTimeout = idleTimeout
	cfg.PacketQueueSize = packetQueueSize
	cfg.BufferSize = bufferSize
	cfg.DialTimeout = dialTimeout
	cfg.MetricsPort = metricsPort

	rtspproxy.SetVerbose(verbose)

	if err := rtspproxy.StartMetricsServer(metricsPort); err != nil {
		log.Fatalf("metrics server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	server := rtspproxy.NewServer(ctx)

	err := server.Listen(portNum)
	if err != nil {
		rtspproxy.LogCriticalf("Failed to bind port: %d, error: %v", portNum, err)
		os.Exit(1)
	}
	rtspproxy.LogCriticalf("Listening on port: %d", portNum)

	go server.Start()

	select {
	case sig := <-sigChan:
		rtspproxy.LogCriticalf("Received signal: %v. Shutting down...", sig)
	case <-ctx.Done():
		rtspproxy.LogCriticalf("Context cancelled. Shutting down...")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		rtspproxy.LogCriticalf("Server shutdown error: %v", err)
		os.Exit(1)
	}

	if err := rtspproxy.ShutdownMetricsServer(shutdownCtx); err != nil {
		rtspproxy.LogCriticalf("Metrics shutdown error: %v", err)
	}

	rtspproxy.LogCriticalf("Server gracefully stopped.")
	os.Exit(0)
}
