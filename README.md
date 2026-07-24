# RTSP/1.0 proxy

Proxy RTSP/RTP connections to real RTSP IP-cameras to save bandwidth.
Based on [djwackey/dorsvr](https://github.com/djwackey/dorsvr/ "dorsvr source code page").

## Synopsis

rtsp://127.0.0.1:8554/rtsp/[login:password@]host[:port]/path

where:

    127.0.0.1:8554 - RTSP server host and port
    /rtsp/ - proxied scheme ()
    login:password - credentials for remote IP RTSP camera
    host - IP/host of target IP camera
    port - use different port for IP camera, by default 554
    /path - profile endpoint

## Features / Improvements

-   **Multi-client support**: Multiple clients can connect to the same RTSP stream from a remote camera, with the proxy efficiently fanning out the media data.
-   **Intelligent Caching**: Caches OPTIONS and SDP responses to reduce requests to the remote camera, improving performance and reducing load.
-   **Authentication Handling**: Improved Digest authentication retry mechanism, including clearing stale nonces for better compatibility with various cameras (e.g., Hikvision).
-   **SDP/RTP-Info Rewriting**: Automatically rewrites IP addresses in SDP and RTP-Info headers to reflect the proxy's address, ensuring clients connect correctly.
-   **Keep-Alive Mechanism**: Implements a session keep-alive using GET_PARAMETER requests to maintain active sessions with remote cameras.
-   **Subscriber Management**: Monitors active subscribers and tears down remote sessions when no clients are connected, conserving resources.
-   **Interleaved Data Handling**: Properly processes and forwards interleaved RTP/RTCP binary data.
-   **Robust Error Handling**: Enhanced logging and error recovery for client and remote connections.

## TODO

-   Add support for UDP transport (currently only TCP interleaved is supported).
-   Implement more robust error handling and graceful shutdown.
-   Improve logging verbosity configuration.
-   Consider adding support for other authentication schemes if needed.
