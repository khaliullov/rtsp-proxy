package rtspproxy

import (
	"net"
)

// Substream represents a single media substream within an RTSP transport.
type Substream struct {
	substreamName string
	transport     *Transport
	Port          int
	Channel       int
	Host          string
	Listener      *net.TCPConn
	Seq           int
	RTPTime       int
}

// NewSubstream creates a new Substream instance.
func NewSubstream(transport *Transport, substreamName string) *Substream {
	substream := &Substream{
		substreamName: substreamName,
		transport:     transport,
		Channel:       -1,
	}
	return substream
}
