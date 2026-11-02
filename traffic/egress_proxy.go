package traffic

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type egressConfig struct {
	upstream    *url.URL
	directRetry bool
}

// EgressProxy is the loopback entry for target traffic when a global proxy is configured.
type EgressProxy struct {
	listener net.Listener
	server   *http.Server
	config   atomic.Pointer[egressConfig]
}

func OpenEgressProxy() (*EgressProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &EgressProxy{listener: ln}
	p.config.Store(&egressConfig{})
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.server.Serve(ln) }()
	return p, nil
}

func (p *EgressProxy) Addr() string { return "http://" + p.listener.Addr().String() }

func (p *EgressProxy) SetUpstream(raw string, directRetry bool) error {
	var upstream *url.URL
	if strings.TrimSpace(raw) != "" {
		var err error
		upstream, err = ValidateProxyURL(raw)
		if err != nil {
			return err
		}
	}
	p.config.Store(&egressConfig{upstream: upstream, directRetry: directRetry})
	return nil
}

func (p *EgressProxy) Close() error { return p.server.Close() }

func (p *EgressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := p.config.Load()
	if r.Method == http.MethodConnect {
		p.serveConnect(w, r, cfg)
		return
	}
	p.serveHTTP(w, r, cfg)
}

func (p *EgressProxy) serveConnect(w http.ResponseWriter, r *http.Request, cfg *egressConfig) {
	address := r.Host
	if _, _, err := net.SplitHostPort(address); err != nil {
		http.Error(w, "invalid CONNECT target", http.StatusBadRequest)
		return
	}
	conn, proxyFault, err := dialEgress(r.Context(), cfg.upstream, address)
	if err != nil && proxyFault && cfg.directRetry {
		log.Printf("[egress] direct retry after proxy connection failure: target=%s", address)
		conn, err = directEgressDial(r.Context(), address)
	}
	if err != nil {
		http.Error(w, "target connection failed", http.StatusBadGateway)
		return
	}
	defer conn.Close()
	client, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	if err := rw.Flush(); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(conn, rw)
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		close(done)
	}()
	_, _ = io.Copy(client, conn)
	_ = client.Close()
	<-done
}

func (p *EgressProxy) serveHTTP(w http.ResponseWriter, r *http.Request, cfg *egressConfig) {
	if r.URL == nil || r.URL.Scheme != "http" || r.URL.Host == "" {
		http.Error(w, "invalid proxy target", http.StatusBadRequest)
		return
	}
	address := r.URL.Host
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(r.URL.Hostname(), "80")
	}
	conn, proxyFault, err := dialHTTPRoute(r.Context(), cfg.upstream, address)
	useHTTPProxy := cfg.upstream != nil && (cfg.upstream.Scheme == "http" || cfg.upstream.Scheme == "https")
	if err != nil && proxyFault && cfg.directRetry {
		log.Printf("[egress] direct retry after proxy connection failure: target=%s", r.URL.Hostname())
		conn, err = directEgressDial(r.Context(), address)
		useHTTPProxy = false
	}
	if err != nil {
		http.Error(w, "target connection failed", http.StatusBadGateway)
		return
	}
	defer conn.Close()
	req := r.Clone(r.Context())
	req.RequestURI = ""
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	if useHTTPProxy {
		if auth := proxyBasicAuth(cfg.upstream); auth != "" {
			req.Header.Set("Proxy-Authorization", auth)
		}
		err = req.WriteProxy(conn)
	} else {
		err = req.Write(conn)
	}
	if err != nil {
		http.Error(w, "target request failed", http.StatusBadGateway)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		http.Error(w, "target response failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if useHTTPProxy && resp.StatusCode == http.StatusProxyAuthRequired && cfg.directRetry && (r.Body == nil || r.Body == http.NoBody) {
		log.Printf("[egress] direct retry after proxy authentication failure: target=%s", r.URL.Hostname())
		p.serveHTTP(w, r, &egressConfig{})
		return
	}
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func directEgressDial(ctx context.Context, address string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
}

func dialEgress(ctx context.Context, upstream *url.URL, address string) (net.Conn, bool, error) {
	if upstream == nil {
		conn, err := directEgressDial(ctx, address)
		return conn, false, err
	}
	if upstream.Scheme == "socks5" {
		return dialSocks5(ctx, upstream, address)
	}
	conn, err := dialProxyEndpoint(ctx, upstream)
	if err != nil {
		return nil, true, err
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if auth := proxyBasicAuth(upstream); auth != "" {
		req.Header.Set("Proxy-Authorization", auth)
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, false, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		conn.Close()
		return nil, false, err
	}
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, resp.StatusCode == http.StatusProxyAuthRequired, fmt.Errorf("proxy CONNECT status %d", resp.StatusCode)
	}
	return &egressBufferedConn{Conn: conn, reader: reader}, false, nil
}

func dialHTTPRoute(ctx context.Context, upstream *url.URL, address string) (net.Conn, bool, error) {
	if upstream == nil {
		conn, err := directEgressDial(ctx, address)
		return conn, false, err
	}
	if upstream.Scheme == "socks5" {
		return dialSocks5(ctx, upstream, address)
	}
	conn, err := dialProxyEndpoint(ctx, upstream)
	return conn, err != nil, err
}

func dialProxyEndpoint(ctx context.Context, upstream *url.URL) (net.Conn, error) {
	address := upstream.Host
	if upstream.Port() == "" {
		port := "80"
		if upstream.Scheme == "https" {
			port = "443"
		}
		address = net.JoinHostPort(upstream.Hostname(), port)
	}
	conn, err := directEgressDial(ctx, address)
	if err != nil {
		return nil, err
	}
	if upstream.Scheme == "https" {
		secured := tls.Client(conn, &tls.Config{ServerName: upstream.Hostname()})
		if err := secured.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = secured
	}
	return conn, nil
}

func proxyBasicAuth(upstream *url.URL) string {
	if upstream == nil || upstream.User == nil {
		return ""
	}
	password, _ := upstream.User.Password()
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(upstream.User.Username()+":"+password))
}

// dialSocks5 keeps the proxy handshake distinct from its target CONNECT reply.
func dialSocks5(ctx context.Context, upstream *url.URL, address string) (net.Conn, bool, error) {
	conn, err := dialProxyEndpoint(ctx, upstream)
	if err != nil {
		return nil, true, err
	}
	fail := func(proxyFault bool, err error) (net.Conn, bool, error) {
		conn.Close()
		return nil, proxyFault, err
	}
	deadline := time.Now().Add(10 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	methods := []byte{5, 1, 0}
	if upstream.User != nil {
		methods = []byte{5, 2, 0, 2}
	}
	if _, err := conn.Write(methods); err != nil {
		return fail(true, err)
	}
	var selected [2]byte
	if _, err := io.ReadFull(conn, selected[:]); err != nil {
		return fail(true, err)
	}
	if selected[0] != 5 || selected[1] == 255 {
		return fail(true, errors.New("SOCKS5 proxy handshake failed"))
	}
	if selected[1] == 2 {
		if upstream.User == nil {
			return fail(true, errors.New("SOCKS5 proxy authentication required"))
		}
		user := upstream.User.Username()
		password, _ := upstream.User.Password()
		if len(user) > 255 || len(password) > 255 {
			return fail(false, errors.New("SOCKS5 proxy credentials too long"))
		}
		auth := append([]byte{1, byte(len(user))}, user...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, password...)
		if _, err := conn.Write(auth); err != nil {
			return fail(true, err)
		}
		var result [2]byte
		if _, err := io.ReadFull(conn, result[:]); err != nil {
			return fail(true, err)
		}
		if result[0] != 1 || result[1] != 0 {
			return fail(true, errors.New("SOCKS5 proxy authentication failed"))
		}
	} else if selected[1] != 0 {
		return fail(true, errors.New("SOCKS5 proxy chose unsupported authentication"))
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fail(false, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fail(false, errors.New("invalid SOCKS5 target port"))
	}
	request := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			request = append(request, 1)
			request = append(request, v4...)
		} else {
			request = append(request, 4)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return fail(false, errors.New("SOCKS5 target host too long"))
		}
		request = append(request, 3, byte(len(host)))
		request = append(request, host...)
	}
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		return fail(false, err)
	}
	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return fail(false, err)
	}
	if reply[0] != 5 {
		return fail(false, errors.New("invalid SOCKS5 proxy reply"))
	}
	remaining := 0
	switch reply[3] {
	case 1:
		remaining = 4
	case 4:
		remaining = 16
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return fail(false, err)
		}
		remaining = int(length[0])
	default:
		return fail(false, errors.New("invalid SOCKS5 proxy address"))
	}
	if _, err := io.CopyN(io.Discard, conn, int64(remaining+2)); err != nil {
		return fail(false, err)
	}
	if reply[1] != 0 {
		return fail(false, fmt.Errorf("SOCKS5 target connect failed (%d)", reply[1]))
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, false, nil
}

type egressBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *egressBufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
