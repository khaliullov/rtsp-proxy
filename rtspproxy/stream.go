package rtspproxy

import (
	"container/list"
)

type Stream struct {
	streamName		string
	SDP				string
	Options			string
	Server			string
	Transports		*list.List  // map[string]*Transport
}

func NewStream(streamName string) *Stream {
	stream := &Stream{
		streamName: streamName,
		Transports: list.New(),
	}
	return stream
}

func (stream *Stream) LookupTransport(substreamName, protocol, comType string) *Transport {
	for e := stream.Transports.Front(); e != nil; e = e.Next() {
		transport := e.Value.(*Transport)
		if transport.SubstreamName == substreamName && transport.Protocol == protocol && transport.ComType == comType {
			return transport
		}
	}

	transport := NewTransport(stream, substreamName, protocol, comType)
	stream.Transports.PushBack(transport)

	return transport
}

func (stream *Stream) LookupTransportBySession(session string) *list.List {
	transports := list.New()
	for e := stream.Transports.Front(); e != nil; e = e.Next() {
		transport := e.Value.(*Transport)
		if transport.Session == session {
			transports.PushBack(transport)
		}
	}
	return transports
}
