package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"
)

// Server runs the embedded DNS resolver and HTTP reverse proxy.
type Server struct {
	cfg    Config
	routes map[string]int // sanitized app name -> port
	mu     sync.RWMutex
}

func newServer(cfg Config) *Server {
	return &Server{
		cfg:    cfg,
		routes: make(map[string]int),
	}
}

// loadRoutes reads the portmap file and populates the route table.
func (s *Server) loadRoutes() {
	mappings := loadPortmap(s.cfg.PortmapFile)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = make(map[string]int)
	for _, pm := range mappings {
		name := sanitizeName(pm.App)
		s.routes[name] = pm.Port
	}
}

// --- DNS server (pure stdlib, zero dependencies) ---

func (s *Server) startDNS() error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.cfg.DNSPort)
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("dns: %w", err)
	}
	defer conn.Close()
	log.Printf("DNS listening on %s (*.test -> 127.0.0.1)", addr)

	buf := make([]byte, 512)
	for {
		n, remote, err := conn.ReadFrom(buf)
		if err != nil {
			continue
		}
		resp := s.handleDNS(buf[:n])
		if resp != nil {
			if _, err := conn.WriteTo(resp, remote); err != nil {
				log.Printf("DNS response failed: %v", err)
			}
		}
	}
}

func (s *Server) handleDNS(query []byte) []byte {
	if len(query) < 12 {
		return nil
	}

	id := binary.BigEndian.Uint16(query[0:2])
	qdcount := binary.BigEndian.Uint16(query[4:6])
	if qdcount != 1 {
		return nil
	}

	// Parse first question
	name, qEnd := readDNSName(query, 12)
	if qEnd < 0 || qEnd+4 > len(query) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(query[qEnd : qEnd+2])
	qclass := binary.BigEndian.Uint16(query[qEnd+2 : qEnd+4])
	questionBytes := query[12 : qEnd+4]

	isMatch := qtype == 1 && qclass == 1 && strings.HasSuffix(strings.ToLower(name), ".test.")

	// Build response
	resp := make([]byte, 0, 128)

	// Header (12 bytes)
	hdr := make([]byte, 12)
	binary.BigEndian.PutUint16(hdr[0:2], id)
	if isMatch {
		binary.BigEndian.PutUint16(hdr[2:4], 0x8400) // QR=1, AA=1, RCODE=0
		binary.BigEndian.PutUint16(hdr[6:8], 1)      // ANCOUNT=1
	} else {
		binary.BigEndian.PutUint16(hdr[2:4], 0x8403) // QR=1, AA=1, RCODE=3 (NXDOMAIN)
	}
	binary.BigEndian.PutUint16(hdr[4:6], qdcount) // QDCOUNT
	resp = append(resp, hdr...)

	// Question section (echo back)
	resp = append(resp, questionBytes...)

	// Answer section
	if isMatch {
		// Name pointer to question at offset 12
		resp = append(resp, 0xC0, 0x0C)
		// Type A (1), Class IN (1)
		resp = append(resp, 0, 1, 0, 1)
		// TTL = 60
		ttl := make([]byte, 4)
		binary.BigEndian.PutUint32(ttl, 60)
		resp = append(resp, ttl...)
		// RDLENGTH = 4, RDATA = 127.0.0.1
		resp = append(resp, 0, 4, 127, 0, 0, 1)
	}

	return resp
}

// readDNSName parses a DNS name from wire format, handling label compression.
func readDNSName(buf []byte, offset int) (string, int) {
	var parts []string
	pos, end, length := offset, -1, 0
	visited := make(map[int]bool)
	for {
		if pos < 0 || pos >= len(buf) || visited[pos] {
			return "", -1
		}
		visited[pos] = true
		n := int(buf[pos])
		if n == 0 {
			if end < 0 {
				end = pos + 1
			}
			return strings.Join(parts, ".") + ".", end
		}
		if n&0xC0 == 0xC0 {
			if pos+1 >= len(buf) {
				return "", -1
			}
			if end < 0 {
				end = pos + 2
			}
			pos = int(binary.BigEndian.Uint16(buf[pos:pos+2]) & 0x3FFF)
			continue
		}
		if n > 63 || pos+1+n >= len(buf) {
			return "", -1
		}
		length += n + 1
		if length > 254 {
			return "", -1
		}
		parts = append(parts, string(buf[pos+1:pos+1+n]))
		pos += n + 1
	}
}

// proxyHandler accepts only configured .test hosts and forwards to loopback.
func (s *Server) proxyHandler() http.Handler {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 15 * time.Second
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = r.In.URL.Host
			r.Out.Host = r.In.Host
			r.SetXForwarded()
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if !strings.HasSuffix(host, ".test") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimSuffix(host, ".test")
		s.mu.RLock()
		port, ok := s.routes[name]
		s.mu.RUnlock()
		if !ok || port < 1 || port > 65535 {
			http.NotFound(w, r)
			return
		}
		r.URL.Host = net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
		r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
		proxy.ServeHTTP(w, r)
	})
}

func (s *Server) startProxy() error {
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(s.cfg.ProxyPort))
	srv := &http.Server{
		Addr: addr, Handler: s.proxyHandler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	log.Printf("HTTP proxy listening on %s", addr)
	return srv.ListenAndServe()
}

// run starts both DNS and HTTP servers.
func (s *Server) run() error {
	s.loadRoutes()

	errCh := make(chan error, 2)
	go func() { errCh <- s.startDNS() }()
	go func() { errCh <- s.startProxy() }()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case err := <-errCh:
			return err
		case <-ticker.C:
			s.loadRoutes()
		}
	}
}
