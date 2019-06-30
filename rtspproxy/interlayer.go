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

/* 2019/06/26 13:59:34 Got request: OPTIONS rtsp://192.168.20.2/profile1 RTSP/1.0
User-Agent: Lavf58.12.100
CSeq: 1

2019/06/26 13:59:34 Sending request: OPTIONS rtsp://192.168.20.2/profile1 RTSP/1.0
CSeq: 1
User-Agent: Lavf58.12.100

2019/06/26 13:59:34 Got response: RTSP/1.0 200 OK
Server: Customer RTSP Server/1.0.0
CSeq: 1
Public: OPTIONS, DESCRIBE, SETUP, TEARDOWN, PLAY, PAUSE, GET_PARAMETER, SET_PARAMETER

2019/06/26 13:59:34 Received 126 new bytes of request data.
2019/06/26 13:59:34 Got request: DESCRIBE rtsp://192.168.20.2/profile1 RTSP/1.0
Accept: application/sdp
CSeq: 2
User-Agent: Lavf58.12.100

2019/06/26 13:59:34 Sending request: DESCRIBE rtsp://192.168.20.2/profile1 RTSP/1.0
CSeq: 2
User-Agent: Lavf58.12.100
Accept: application/sdp

2019/06/26 13:59:34 Got request: DESCRIBE rtsp://192.168.20.2/profile1 RTSP/1.0
Accept: application/sdp
CSeq: 2
User-Agent: Lavf58.12.100
Authorization: Digest username="admin", realm="RTSP SERVER", nonce="9dbbfa3a2bc8a69a40c170c580fc8d06", uri="rtsp://192.168.20.2/profile1", response="af83377dee97cdd8aa54486370a963ae"

2019/06/26 13:59:34 Sending request: DESCRIBE rtsp://192.168.20.2/profile1 RTSP/1.0
Accept: application/sdp
CSeq: 3
User-Agent: Lavf58.12.100
Authorization: Digest username="admin", realm="RTSP SERVER", nonce="9dbbfa3a2bc8a69a40c170c580fc8d06", uri="rtsp://192.168.20.2/profile1", response="af83377dee97cdd8aa54486370a963ae"

2019/06/26 13:59:34 Got response: RTSP/1.0 200 OK
x-Accept-Dynamic-Rate: 1
Content-Length: 418
Content-Base: rtsp://192.168.1.8:8554/rtsp/admin:12345678@192.168.20.2:554/profile1/
Last-Modified: Fri Mar 23 16:21:08 2018 GMT
Date: Wed, Jun 26 2019 13:59:38 GMT
Content-Type: application/sdp
Expires: Wed, Jun 26 2019 13:59:38 GMT
x-Accept-Retransmit: our-retransmit
Server: Customer RTSP Server/1.0.0
CSeq: 2
Cache-Control: must-revalidate

v=0
o=- 1561557578354164 1 IN IP4 192.168.20.2
s=\profile1
u=http:///
e=admin@
t=0 0
a=control:*
a=range:npt=00.000-
m=video 0 RTP/AVP 96
b=AS:5000
a=control:track1
a=rtpmap:96 H264/90000
a=fmtp:96 profile-level-id=676400; sprop-parameter-sets=Z2QAH62EAQwgCGEAQwgCGEAQwgCEK1AoAt03AQEBAg==,aO4xshs=; packetization-mode=1
m=audio 0 RTP/AVP 8
b=AS:1000
a=control:track2
a=rtpmap:8 pcma/8000
a=ptime:40
2019/06/26 13:59:34 Received 152 new bytes of request data.
2019/06/26 13:59:34 Got request: SETUP rtsp://192.168.20.2/profile1/track1 RTSP/1.0
CSeq: 3
User-Agent: Lavf58.12.100
Authorization: Digest username="admin", realm="RTSP SERVER", nonce="9dbbfa3a2bc8a69a40c170c580fc8d06", uri="rtsp://192.168.20.2/profile1/track1", response="c2f31c02370d45206a8ff4795c1080b2"
Transport: RTP/AVP/TCP;unicast
x-Dynamic-Rate: 0

2019/06/26 13:59:34 Sending request: SETUP rtsp://192.168.20.2/profile1/track1 RTSP/1.0
Transport: RTP/AVP/TCP;unicast
x-Dynamic-Rate: 0
CSeq: 4
User-Agent: Lavf58.12.100
Authorization: Digest username="admin", realm="RTSP SERVER", nonce="9dbbfa3a2bc8a69a40c170c580fc8d06", uri="rtsp://192.168.20.2/profile1/track1", response="c2f31c02370d45206a8ff4795c1080b2"

2019/06/26 13:59:34 Got response: RTSP/1.0 200 OK
Cache-Control: must-revalidate
Date: Wed, Jun 26 2019 13:59:38 GMT
Expires: Wed, Jun 26 2019 13:59:38 GMT
Transport: RTP/AVP/TCP;unicast;destination=::ffff:192.168.88.254;source=192.168.20.2;interleaved=0-1;ssrc=401caf81
Session: 410523494695999;timeout=60
Server: Customer RTSP Server/1.0.0
CSeq: 3
Last-Modified: Fri Mar 23 16:21:08 2018 GMT

2019/06/26 13:59:34 Received 198 new bytes of request data.
2019/06/26 13:59:34 Got request: SETUP rtsp://192.168.20.2/profile1/track2 RTSP/1.0
CSeq: 4
User-Agent: Lavf58.12.100
Session: 410523494695999
Authorization: Digest username="admin", realm="RTSP SERVER", nonce="9dbbfa3a2bc8a69a40c170c580fc8d06", uri="rtsp://192.168.20.2/profile1/track2", response="1f675478d26d2352dd04300424eff6a8"
Transport: RTP/AVP/TCP;unicast
x-Dynamic-Rate: 0

2019/06/26 13:59:34 Sending request: SETUP rtsp://192.168.20.2/profile1/track2 RTSP/1.0
Transport: RTP/AVP/TCP;unicast
x-Dynamic-Rate: 0
CSeq: 5
User-Agent: Lavf58.12.100
Session: 410523494695999
Authorization: Digest username="admin", realm="RTSP SERVER", nonce="9dbbfa3a2bc8a69a40c170c580fc8d06", uri="rtsp://192.168.20.2/profile1/track2", response="1f675478d26d2352dd04300424eff6a8"

2019/06/26 13:59:34 Got response: RTSP/1.0 200 OK
Session: 410523494695999;timeout=60
Server: Customer RTSP Server/1.0.0
CSeq: 4
Last-Modified: Fri Mar 23 16:21:08 2018 GMT
Cache-Control: must-revalidate
Date: Wed, Jun 26 2019 13:59:38 GMT
Expires: Wed, Jun 26 2019 13:59:38 GMT
Transport: RTP/AVP/TCP;unicast;destination=::ffff:192.168.88.254;source=192.168.20.2;interleaved=2-3;ssrc=d6fdedf

2019/06/26 13:59:34 Received 224 new bytes of request data.
2019/06/26 13:59:34 Got request: PLAY rtsp://192.168.20.2/profile1/ RTSP/1.0
Range: npt=0.000-
CSeq: 5
User-Agent: Lavf58.12.100
Session: 410523494695999
Authorization: Digest username="admin", realm="RTSP SERVER", nonce="9dbbfa3a2bc8a69a40c170c580fc8d06", uri="rtsp://192.168.20.2/profile1/", response="4df3822d332d40a242576001a9b58016"

2019/06/26 13:59:34 Sending request: PLAY rtsp://192.168.20.2/profile1/ RTSP/1.0
Session: 410523494695999
Authorization: Digest username="admin", realm="RTSP SERVER", nonce="9dbbfa3a2bc8a69a40c170c580fc8d06", uri="rtsp://192.168.20.2/profile1/", response="4df3822d332d40a242576001a9b58016"
Range: npt=0.000-
CSeq: 6
User-Agent: Lavf58.12.100

2019/06/26 13:59:34 Got response: RTSP/1.0 200 OK
Server: Customer RTSP Server/1.0.0
CSeq: 5
Session: 410523494695999
Range: npt=0.000-
RTP-Info: url=rtsp://192.168.20.2/profile1/track1;seq=52326;rtptime=1781120107,url=rtsp://192.168.20.2/profile1/track2;seq=44529;rtptime=572932177

2019/06/26 13:59:34 Received 169 new bytes of request data. */