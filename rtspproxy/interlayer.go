package rtspproxy

import (
	"container/list"
)

// Interlayer represents an interleaved data channel for RTSP.
type Interlayer struct {
	Channel     int
	Stream      *Stream
	Substream   *Substream
	Transport   *Transport
	Subscribers *list.List
}

// NewInterlayer creates a new Interlayer instance.
func NewInterlayer(channel int, stream *Stream, transport *Transport, substream *Substream) *Interlayer {
	interlayer := &Interlayer{
		Channel:     channel,
		Stream:      stream,
		Substream:   substream,
		Transport:   transport,
		Subscribers: list.New(),
	}
	return interlayer
}
