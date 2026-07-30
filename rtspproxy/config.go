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

	// Metrics HTTP endpoint (0 = disabled)
	MetricsPort int
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		DialTimeout:  5 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 5 * time.Second,
		KeepaliveInt: 55 * time.Second,

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
		MetricsPort:     0,
	}
}

// GlobalConfig is the process-wide configuration. Mutate only before Server starts.
var GlobalConfig = DefaultConfig()

// Validate ensures configuration parameters are within sane bounds.
func (c *Config) Validate() error {
	if c.DialTimeout <= 0 {
		c.DialTimeout = 5 * time.Second
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = 1 * time.Second
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 5 * time.Second
	}
	if c.PacketQueueSize <= 0 {
		c.PacketQueueSize = 1000
	}
	if c.BufferSize < 4096 {
		c.BufferSize = 65536
	}
	return nil
}
