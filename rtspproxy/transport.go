package rtspproxy

import "sync"

// Transport represents an RTSP transport for a media stream.
type Transport struct {
	SubstreamName string
	Session       *Session
	Protocol      string // RTP/AVP/TCP or RTP/AVP
	ComType       string // always unicast
	Ssrc          string
	Substreams    map[int]*Substream
	mu            sync.RWMutex
}

// NewTransport creates a new Transport instance.
func NewTransport(session *Session, substreamName, protocol, comType string) *Transport {
	transport := &Transport{
		SubstreamName: substreamName,
		Session:       session,
		Protocol:      protocol,
		ComType:       comType,
		Substreams:    make(map[int]*Substream),
	}
	return transport
}
