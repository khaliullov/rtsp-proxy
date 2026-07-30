package rtspproxy

import (
	"context"
	"time"
)

// Ipc represents an Inter-Process Communication mechanism.
type Ipc struct {
	Channel chan string
	timeout int
}

// NewIPC creates a new Ipc instance.
func NewIPC(timeout ...int) *Ipc {
	defautlTimeout := 10
	if len(timeout) > 0 {
		defautlTimeout = timeout[0]
	}
	ipc := &Ipc{
		// Buffered channel ensures the sender (readLoop) never blocks.
		Channel: make(chan string, 1),
		timeout: defautlTimeout,
	}
	return ipc
}

// GetResponse waits for a response on the IPC channel or times out/cancels.
func (ipc *Ipc) GetResponse(ctx context.Context) string {
	toSleep := time.Duration(ipc.timeout) * time.Second
	timer := time.NewTimer(toSleep)
	defer timer.Stop()
	defer close(ipc.Channel)

	select {
	case res := <-ipc.Channel:
		return res
	case <-ctx.Done():
		return "timeout" // Cancelled
	case <-timer.C:
		return "timeout"
	}
}
