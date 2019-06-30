package rtspproxy

import (
	"net"
)

type Substream struct {
	substreamName string
	transport     Transport
	Port          int
	Channel       int
	Host          string
	Listener      *net.TCPConn
	Seq           int
	RTPTime       int
}

func NewSubstream(transport *Transport, substreamName string) *Substream {
	substream := &Substream{
		substreamName: substreamName,
		Channel: -1,
	}
	return substream
}
