# RTSP/1.0 proxy

Proxy RTSP/RTP connections to real RTSP IP-cameras to save bandwidth.
Based on [djwackey/dorsvr](https://github.com/djwackey/dorsvr/ "dorsvr source code page").

**!!! Under construction !!!**

## Synopsis

rtsp://127.0.0.1:8554/rtsp/[login:password@]host[:port]/path

where:

    127.0.0.1:8554 - RTSP server host and port
    /rtsp/ - proxied scheme ()
    login:password - credentials for remote IP RTSP camera
    host - IP/host of target IP camera
    port - use different port for IP camera, by default 554
    /path - profile endpoint

## TODO

- overwrite IP-addresses in responses (RTP-Info, Transport headers), SDP
- process RTCP requests
- RTP connections
