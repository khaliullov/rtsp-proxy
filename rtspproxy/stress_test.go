//go:build stress

package rtspproxy

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlowClientIsolationStress(t *testing.T) {
	GlobalConfig.PacketQueueSize = 10000 // Large enough for test
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

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Respond to setup
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
			} else if req[:5] == "SETUP" {
				conn.Write([]byte("RTSP/1.0 200 OK\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1;ssrc=12345678\r\nSession: 1234\r\nCSeq: 3\r\n\r\n"))
			} else if req[:4] == "PLAY" {
				conn.Write([]byte("RTSP/1.0 200 OK\r\nRTP-Info: url=rtsp://127.0.0.1/mock/track1;seq=1;rtptime=1\r\nSession: 1234\r\nCSeq: 4\r\n\r\n"))
				// Push huge volume of packets
				for i := 0; i < 5000; i++ {
					pkt := []byte{'$', 0, 0, 4, 1, 2, 3, 4}
					_, err := conn.Write(pkt)
					if err != nil {
						return
					}
				}
			}
		}
	}()

	stream := server.LookupStream(camAddr, "", "", "/mock")

	// Fast client
	cln1, _ := net.Listen("tcp", "127.0.0.1:0")
	var fastRecv atomic.Uint64
	go func() {
		c, err := net.Dial("tcp", cln1.Addr().String())
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 1024)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			for i := 0; i < n; i++ {
				if buf[i] == '$' {
					fastRecv.Add(1)
				}
			}
		}
	}()
	conn1, _ := cln1.Accept()
	client1 := NewClient(server, conn1)
	stream.AddClient(client1, "sess1")
	stream.MapChannel(client1, 0, 0)

	// Slow client (doesn't read)
	cln2, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		c, _ := net.Dial("tcp", cln2.Addr().String())
		defer c.Close()
		// Dial and block
		select {}
	}()
	conn2, _ := cln2.Accept()
	client2 := NewClient(server, conn2)
	stream.AddClient(client2, "sess2")
	stream.MapChannel(client2, 0, 0)

	time.Sleep(2 * time.Second)

	received := fastRecv.Load()
	dropped := atomic.LoadUint64(&stream.PacketsDropped)
	forwarded := atomic.LoadUint64(&stream.PacketsForwarded)

	fmt.Printf("TEST: Received: %d, Dropped: %d, Forwarded: %d\n", received, dropped, forwarded)

	if received < 1000 {
		t.Errorf("fast client only received %d packets (Dropped: %d, Forwarded: %d), should be more", received, dropped, forwarded)
	}

	stream.mu.RLock()
	if _, ok := stream.clients[client2]; ok {
		// client2 should have been dropped by ClientSession due to full queue
		// Wait, ClientSession.run() disconnects the client.
	}
	stream.mu.RUnlock()
}

func TestConcurrentConnectStress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := NewServer(ctx)

	// Mock camera
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	camAddr := ln.Addr().String()
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
					if req[:7] == "OPTIONS" {
						c.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 1\r\n\r\n"))
					} else if req[:8] == "DESCRIBE" {
						sdp := "v=0\r\na=control:track1\r\n"
						c.Write([]byte(fmt.Sprintf("RTSP/1.0 200 OK\r\nContent-Length: %d\r\nCSeq: 2\r\n\r\n%s", len(sdp), sdp)))
					} else if req[:5] == "SETUP" {
						c.Write([]byte("RTSP/1.0 200 OK\r\nSession: 123\r\nCSeq: 3\r\n\r\n"))
					} else if req[:4] == "PLAY" {
						c.Write([]byte("RTSP/1.0 200 OK\r\nCSeq: 4\r\n\r\n"))
					}
				}
			}(conn)
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			stream := server.LookupStream(camAddr, "", "", "/mock")
			stream.Start()
			time.Sleep(10 * time.Millisecond)
			stream.GetState()
		}(i)
	}
	wg.Wait()
}
