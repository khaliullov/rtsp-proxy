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
	flag.StringVar(&logFile, "log", "-", "log file")
	flag.IntVar(&portNum, "port", 554, "server port")
	flag.BoolVar(&verbose, "verbose", false, "enable verbose logging")
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

	rtspproxy.SetVerbose(verbose) // Set the verbose flag in the rtspproxy package

	// Create a context that can be cancelled to signal shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure cancel is called eventually

	// Listen for OS signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	server := rtspproxy.NewServer(ctx) // Pass the context to the server

	err := server.Listen(portNum)
	if err != nil {
		rtspproxy.LogCriticalf("Failed to bind port: %d, error: %v", portNum, err)
		os.Exit(1)
	}
	rtspproxy.LogCriticalf("Listening on port: %d", portNum)

	go server.Start()

	// Block until a signal is received or context is cancelled
	select {
	case sig := <-sigChan:
		rtspproxy.LogCriticalf("Received signal: %v. Shutting down...", sig)
	case <-ctx.Done():
		rtspproxy.LogCriticalf("Context cancelled. Shutting down...")
	}

	// Initiate graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		rtspproxy.LogCriticalf("Server shutdown error: %v", err)
		os.Exit(1)
	}

	rtspproxy.LogCriticalf("Server gracefully stopped.")
	os.Exit(0)
}
