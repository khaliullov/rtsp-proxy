package rtspproxy

type Transport struct {
	SubstreamName	string
	Session			*Session
	Protocol		string		// RTP/AVP/TCP or RTP/AVP
	ComType			string		// always unicast
	Ssrc			string
	// Session			string
	Substreams		map[int]*Substream
}

func NewTransport(session *Session, substreamName, protocol, comType string) *Transport {
	transport := &Transport{
		SubstreamName: substreamName,
		Session: session,
		Protocol: protocol,
		ComType: comType,
		Substreams: make(map[int]*Substream),
	}
	return transport
}
