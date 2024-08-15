package main

import (
	"bufio"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"

	"golang.org/x/net/proxy"
)

// countingWriter and countingReader
// to record the number of bytes written and read
type countingWriter struct {
	writer       io.Writer
	bytesWritten int64
}

type countingReader struct {
	reader    io.Reader
	bytesRead int64
}

// Write writes the given byte slice to the underlying writer and updates the bytesWritten counter.
//
// Parameter p is the byte slice to be written.
// Returns the number of bytes written and any error that occurred during the write operation.
func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.writer.Write(p)
	atomic.AddInt64(&c.bytesWritten, int64(n))
	return n, err
}

// Read reads from the underlying reader and updates the bytesRead counter.
//
// Parameter p is the byte slice to be read from.
// Returns the number of bytes read and any error that occurred during the read operation.
func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	atomic.AddInt64(&c.bytesRead, int64(n))
	return n, err
}

var blacklist map[string]struct{}

func loadBlacklist() error {
	file, err := os.Open("blacklist.txt")
	if err != nil {
		return err
	}
	defer file.Close()

	blacklist = make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			blacklist[line] = struct{}{}
		}
	}

	return scanner.Err()
}

func isBlacklisted(address string) bool {
	_, blacklisted := blacklist[address]
	return blacklisted
}

func handleHTTP(w http.ResponseWriter, r *http.Request) {
	// log request
	log.Printf("[HTTP] [Client %s], target: %s", r.RemoteAddr, r.URL.String())
	if isBlacklisted(r.URL.Host) {
		w.WriteHeader(http.StatusTeapot)
		return
	}
	client := &http.Client{}

	resp, err := client.Do(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// create countingWriter to record the number of bytes written
	cw := &countingWriter{writer: w}

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)

	io.Copy(cw, resp.Body)
	log.Printf("HTTP request to %s transferred %d bytes\n", r.URL.String(), cw.bytesWritten)
}

func handleSOCKS(conn net.Conn, targetAddr string) {
	// log request
	log.Printf("[SOCKS] [Client %s] target: %s", conn.RemoteAddr().String(), targetAddr)

	host, _, err := net.SplitHostPort(targetAddr)
	if err != nil {
		log.Printf("[SOCKS] [Client %s] Failed to parse target address: %s", conn.RemoteAddr().String(), err)
		return
	}

	if isBlacklisted(host) {
		conn.Write([]byte("HTTP/1.1 418 I'm a teapot\r\n\r\n"))
		conn.Close()
		return
	}
	dialer, err := proxy.SOCKS5("tcp", "127.0.0.1:1080", nil, proxy.Direct)
	if err != nil {
		log.Printf("[SOCKS] [Client %s] Failed to create SOCKS5 dialer: %s", conn.RemoteAddr().String(), err)
		return
	}

	targetConn, err := dialer.Dial("tcp", targetAddr)
	if err != nil {
		log.Printf("[SOCKS] [Client %s] Failed to connect to target: %s", conn.RemoteAddr().String(), err)
		return
	}
	defer targetConn.Close()

	cr := &countingReader{reader: conn}
	cw := &countingWriter{writer: targetConn}

	// 使用 countingReader 和 countingWriter 记录传输的字节数
	go io.Copy(cw, cr)
	io.Copy(conn, targetConn)

	log.Printf("SOCKS request to %s transferred %d bytes in and %d bytes out\n", targetAddr, cr.bytesRead, cw.bytesWritten)
}

func main() {
	err := loadBlacklist()
	if err != nil {
		log.Fatalf("Failed to load blacklist: %v", err)
	}
	http.HandleFunc("/", handleHTTP)

	go func() {
		log.Println("Starting HTTP/HTTPS proxy on :8080")
		log.Fatal(http.ListenAndServe(":8080", nil))
	}()

	listener, err := net.Listen("tcp", ":1081")
	if err != nil {
		log.Fatal("Failed to start SOCKS proxy:", err)
	}
	log.Println("Starting SOCKS proxy on :1081")

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Println("Failed to accept connection:", err)
			continue
		}

		go handleSOCKS(conn, conn.RemoteAddr().String())
	}
}
