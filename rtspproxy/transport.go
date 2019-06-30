package rtspproxy

type Transport struct {
	// transportName	string
	SubstreamName	string
	Stream			*Stream
	Protocol		string		// RTP/AVP/TCP or RTP/AVP
	ComType			string		// always unicast
	Ssrc			string
	Session			string

	// Active			bool
	// Interleaved		bool  Protocol
	Substreams		map[int]*Substream
}

func NewTransport(stream *Stream, substreamName, protocol, comType string) *Transport {
	transport := &Transport{
		SubstreamName: substreamName,
		Stream: stream,
		Protocol: protocol,
		ComType: comType,
		Substreams: make(map[int]*Substream),
	}
	return transport
}


