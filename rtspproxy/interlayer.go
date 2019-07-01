package rtspproxy

import (
	"container/list"
)

type Interlayer struct {
	Channel		int
	Active		bool
	Stream		*Stream
	Substream	*Substream
	Transport   *Transport
	Subscribers	*list.List
}

func NewInterlayer(channel int, stream *Stream, transport *Transport, substream *Substream) *Interlayer {
	interlayer := &Interlayer{
		Channel: channel,
		Stream: stream,
		Substream: substream,
		Transport: transport,
		Subscribers: list.New(),
	}
	return interlayer
}
