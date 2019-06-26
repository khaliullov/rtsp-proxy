package main

import (
	"log"
	"os"

	"github.com/khaliullov/rtsp-proxy/rtspproxy"
)

func main() {
	log.SetOutput(os.Stderr)
	server := rtspproxy.NewServer()

	portNum := 8554
	err := server.Listen(portNum)

	if err != nil {
		log.Printf("Failed to bind port: %d\n", portNum)
		return
	}

	go server.Start()

	select {}
}
