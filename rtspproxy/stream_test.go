package rtspproxy

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestStreamLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := NewServer(ctx)

	// Mock camera server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	camAddr := ln.Addr().String()

	var activeConn net.Conn
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			activeConn = conn
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					req := string(buf[:n])
					if req == "" {
						continue
					}
					if req[:7] == "OPTIONS" {
						c.Write([]byte("RTSP/1.0 200 OK\r\nPublic: OPTIONS, DESCRIBE, SETUP, PLAY\r\nCSeq: 1\r\n\r\n"))
					} else if req[:8] == "DESCRIBE" {
						sdp := "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=Mock\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=video 0 RTP/AVP 96\r\na=control:track1\r\n"
						c.Write([]byte(fmt.Sprintf("RTSP/1.0 200 OK\r\nContent-Type: application/sdp\r\nContent-Length: %d\r\nCSeq: 2\r\n\r\n%s", len(sdp), sdp)))
					} else if req[:5] == "SETUP" {
						c.Write([]byte("RTSP/1.0 200 OK\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1;ssrc=12345678\r\nSession: 1234\r\nCSeq: 3\r\n\r\n"))
					} else if req[:4] == "PLAY" {
						c.Write([]byte("RTSP/1.0 200 OK\r\nRTP-Info: url=rtsp://127.0.0.1/mock/track1;seq=1;rtptime=1\r\nSession: 1234\r\nCSeq: 4\r\n\r\n"))
						go func() {
							for {
								_, err := c.Write([]byte{'$', 0, 0, 4, 1, 2, 3, 4})
								if err != nil {
									return
								}
								time.Sleep(100 * time.Millisecond)
							}
						}()
					}
				}
			}(conn)
		}
	}()

	stream := server.LookupStream(camAddr, "", "", "/mock")
	if stream == nil {
		t.Fatal("failed to lookup stream")
	}

	// Mock client
	cln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		c, _ := net.Dial("tcp", cln.Addr().String())
		buf := make([]byte, 1024)
		for {
			_, err := c.Read(buf)
			if err != nil {
				return
			}
		}
	}()
	conn, _ := cln.Accept()
	client := NewClient(server, conn)

	// Add client to stream
	stream.AddClient(client, "sess1")
	stream.MapChannel(client, 0, 0)

	// Wait for connection
	time.Sleep(2 * time.Second)
	t.Logf("Stream state: %s, Packets forwarded: %d", stream.GetState(), stream.PacketsForwarded)

	if stream.GetState() != StatePlaying {
		t.Errorf("expected state Playing, got %s", stream.GetState())
	}

	// Check if second client receives packets
	pktChan := make(chan []byte, 10)
	cln2, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		c, _ := net.Dial("tcp", cln2.Addr().String())
		buf := make([]byte, 1024)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			if buf[0] == '$' {
				pktChan <- buf[:n]
			}
		}
	}()
	conn2, _ := cln2.Accept()
	client2 := NewClient(server, conn2)
	stream.AddClient(client2, "sess2")
	stream.MapChannel(client2, 0, 0)

	count := 0
	timeout := time.After(2 * time.Second)
L:
	for {
		select {
		case <-pktChan:
			count++
			if count >= 5 {
				break L
			}
		case <-timeout:
			t.Errorf("timeout waiting for packets for client2, received %d", count)
			break L
		}
	}

	// Test reconnect
	t.Log("Killing mock camera...")
	if activeConn != nil {
		activeConn.Close()
	}
	// Re-listen for reconnect
	ln.Close()
	ln, _ = net.Listen("tcp", camAddr)

	time.Sleep(500 * time.Millisecond)
	if stream.GetState() != StateReconnecting {
		t.Errorf("expected state Reconnecting, got %s", stream.GetState())
	}

	// Restart mock camera responder
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					req := string(buf[:n])
					if req == "" {
						continue
					}
					if req[:7] == "OPTIONS" {
						c.Write([]byte("RTSP/1.0 200 OK\r\nPublic: OPTIONS, DESCRIBE, SETUP, PLAY\r\nCSeq: 1\r\n\r\n"))
					} else if req[:8] == "DESCRIBE" {
						sdp := "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=Mock\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=video 0 RTP/AVP 96\r\na=control:track1\r\n"
						c.Write([]byte(fmt.Sprintf("RTSP/1.0 200 OK\r\nContent-Type: application/sdp\r\nContent-Length: %d\r\nCSeq: 2\r\n\r\n%s", len(sdp), sdp)))
					} else if req[:5] == "SETUP" {
						c.Write([]byte("RTSP/1.0 200 OK\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1;ssrc=12345678\r\nSession: 1234\r\nCSeq: 3\r\n\r\n"))
					} else if req[:4] == "PLAY" {
						c.Write([]byte("RTSP/1.0 200 OK\r\nRTP-Info: url=rtsp://127.0.0.1/mock/track1;seq=1;rtptime=1\r\nSession: 1234\r\nCSeq: 4\r\n\r\n"))
						go func() {
							for {
								_, err := c.Write([]byte{'$', 0, 0, 4, 1, 2, 3, 4})
								if err != nil {
									return
								}
								time.Sleep(100 * time.Millisecond)
							}
						}()
					}
				}
			}(conn)
		}
	}()

	// Wait for reconnect backoff (1s)
	time.Sleep(3 * time.Second)
	if stream.GetState() != StatePlaying {
		t.Errorf("expected state Playing after reconnect, got %s", stream.GetState())
	}

	// Test idle disconnect
	t.Log("Testing idle disconnect...")
	stream.IdleTimeout = 1 * time.Second
	stream.RemoveClient(client)
	stream.RemoveClient(client2)

	time.Sleep(2 * time.Second)
	if stream.GetState() != StateDisconnected {
		t.Errorf("expected state Disconnected after idle, got %s", stream.GetState())
	}
}

func TestDescribeOnDemand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := NewServer(ctx)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	camAddr := ln.Addr().String()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			req := string(buf[:n])
			if req == "" {
				continue
			}
			if req[:7] == "OPTIONS" {
				conn.Write([]byte("RTSP/1.0 200 OK\r\nPublic: OPTIONS, DESCRIBE, SETUP, PLAY\r\nCSeq: 1\r\n\r\n"))
			} else if req[:8] == "DESCRIBE" {
				sdp := "v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=Mock\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=video 0 RTP/AVP 96\r\na=control:track1\r\n"
				conn.Write([]byte(fmt.Sprintf("RTSP/1.0 200 OK\r\nContent-Type: application/sdp\r\nContent-Length: %d\r\nCSeq: 2\r\n\r\n%s", len(sdp), sdp)))
			}
		}
	}()

	stream := server.LookupStream(camAddr, "", "", "/mock")

	// Mock client
	cln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		c, _ := net.Dial("tcp", cln.Addr().String())
		c.Close()
	}()
	conn, _ := cln.Accept()
	client := NewClient(server, conn)

	// This should trigger connectLoop and wait for SDP
	resp := client.handleDescribe(stream, &Request{URL: nil}) // URL is not used for getting SDP from stream
	if resp.Code != 200 {
		t.Errorf("expected 200 OK for cold DESCRIBE, got %d", resp.Code)
	}

	if stream.SDP == "" {
		t.Error("SDP should not be empty")
	}
}
