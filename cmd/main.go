package main

import (
	"flag"
	"log"
	"os"

	"github.com/khaliullov/rtsp-proxy/rtspproxy"
)

func main() {
	var logFile string
	var portNum int
	flag.StringVar(&logFile, "log", "-", "log file")
	flag.IntVar(&portNum, "port", 554, "server port")
	flag.Parse()
	if logFile == "-" {
		log.SetOutput(os.Stderr)
	} else {
		f, err := os.OpenFile(logFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			log.Fatalf("error opening log file %s: %v", logFile, err)
		}
		defer f.Close()
		log.SetOutput(f)
	}
	server := rtspproxy.NewServer()

	err := server.Listen(portNum)

	if err != nil {
		log.Printf("Failed to bind port: %d", portNum)
		return
	} else {
		log.Printf("Listening on port: %d", portNum)
	}

	go server.Start()

	select {}
}
