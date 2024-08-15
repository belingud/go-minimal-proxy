package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

// countingConn wraps a net.Conn and counts the number of bytes written and read.
type countingConn struct {
	net.Conn
	bytesWritten int64
	bytesRead    int64
}

// Write wraps the underlying net.Conn's Write method, counting the bytes written.
func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	atomic.AddInt64(&c.bytesWritten, int64(n))
	return n, err
}

// Read wraps the underlying net.Conn's Read method, counting the bytes read.
func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	atomic.AddInt64(&c.bytesRead, int64(n))
	return n, err
}

var blacklist map[string]bool

func loadBlacklist(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	blacklist = make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		blacklist[strings.TrimSpace(scanner.Text())] = true
	}

	return scanner.Err()
}

func isBlocked(host string) bool {
	for blockedURL := range blacklist {
		if strings.HasPrefix(host, blockedURL) {
			return true
		}
	}
	return false
}

func extractIPv4FromRemoteAddr(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	log.Printf("remoteAddr: %s, host: %s", remoteAddr, host)
	if err != nil {
		return remoteAddr // if error, return original remoteAddr
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return remoteAddr
	}

	// if it is an IPv4 address
	if ip.To4() != nil {
		return ip.String()
	}

	// if it is an IPv4-mapped IPv6 address
	if ip.IsLoopback() && strings.HasPrefix(ip.String(), "::ffff:") {
		return ip.String()[7:]
	}

	return remoteAddr
}

func handleClientConnection(client net.Conn) {
	defer client.Close()
	// extract IPv4 from remoteAddr
	remoteAddr := extractIPv4FromRemoteAddr(client.RemoteAddr().String())
	log.Printf("[Client %s] Received connection", remoteAddr)

	// read request
	clientReader := bufio.NewReader(client)
	req, err := http.ReadRequest(clientReader)
	if err != nil {
		log.Printf("[Client %s] Error reading request: %v", remoteAddr, err)
		return
	}

	// only support CONNECT
	if req.Method != "CONNECT" {
		log.Printf("[Client %s]Invalid request method: %s", remoteAddr, req.Method)
		return
	}

	// parse target host and port
	hostPort := req.URL.Host
	log.Printf("[Client %s] Target host: %s", remoteAddr, hostPort)
	if isBlocked(hostPort) {
		log.Printf("[Client: %s] Blocked host: %s", remoteAddr, hostPort)
		// send teapot response
		client.Write([]byte("HTTP/1.1 418 I'm a teapot\r\n\r\n"))
		return
	}
	if !strings.Contains(hostPort, ":") {
		hostPort = hostPort + ":443" // https as default
	}

	// connect to server
	server, err := net.Dial("tcp", hostPort)
	if err != nil {
		log.Printf("[Client %s] Error connecting to %v: %v", remoteAddr, hostPort, err)
		return
	}
	defer server.Close()

	// log data transferred
	clientCounting := &countingConn{Conn: client}
	serverCounting := &countingConn{Conn: server}

	resp := "HTTP/1.1 200 Connection Established\r\n"
	resp += "Proxy-agent: go-tunnel-proxy\r\n"
	resp += "Connection: close\r\n\r\n"
	client.Write([]byte(resp))

	go io.Copy(server, client)
	io.Copy(client, server)

	log.Printf(
		"[Client %s] Data transferred: sent %d bytes, received %d bytes",
		remoteAddr,
		clientCounting.bytesWritten,
		serverCounting.bytesRead,
	)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000" // default port
	}

	err := loadBlacklist("blacklist.txt")
	if err != nil {
		log.Fatalf("Failed to load blacklist: %v", err)
	}
	listenAddr := fmt.Sprintf(":%s", port)
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", listenAddr, err)
		return
	}
	defer listener.Close()

	log.Printf("Listening on %s", listenAddr)
	for {
		client, err := listener.Accept()
		if err != nil {
			log.Println("Error accepting:", err)
			continue
		}

		go handleClientConnection(client)
	}
}
