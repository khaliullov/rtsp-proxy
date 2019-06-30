package rtspproxy

import (
	"time"
)

type Ipc struct {
	Channel			chan string
	timeout			int
}

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
