package rtspproxy

import (
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
		Channel: make(chan string),
		timeout: defautlTimeout,
	}
	return ipc
}

// GetResponse waits for a response on the IPC channel or times out.
func (ipc *Ipc) GetResponse() string {
	toSleep := time.Duration(ipc.timeout) * time.Second
	defer close(ipc.Channel)
	var res string
	select {
	case res = <-ipc.Channel:
		return res
	case <-time.After(toSleep):
		res = "timeout"
	}
	return res
}
