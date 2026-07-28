package rtspproxy

import (
	"time"
)

// Config holds all configurable parameters for the RTSP proxy.
type Config struct {
	// TCP/Network timeouts
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	KeepaliveInt time.Duration

	// Stream lifecycle
	IdleTimeout      time.Duration
	ReconnectBackoff []time.Duration

	// Buffers and Queues
	PacketQueueSize int
	BufferSize      int
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		DialTimeout:  5 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 5 * time.Second,
		KeepaliveInt: 55 * time.Second, // Just under 60s default timeout

		IdleTimeout: 20 * time.Second,
		ReconnectBackoff: []time.Duration{
			1 * time.Second,
			2 * time.Second,
			5 * time.Second,
			10 * time.Second,
			30 * time.Second,
		},

		PacketQueueSize: 1000,
		BufferSize:      65536,
	}
}

var GlobalConfig = DefaultConfig()
