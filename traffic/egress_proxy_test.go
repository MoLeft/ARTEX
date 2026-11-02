package traffic

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func egressTestClient(t *testing.T, p *EgressProxy) *http.Client {
	t.Helper()
	pu, err := url.Parse(p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func TestEgressProxyFallback(t *testing.T) {
	for _, secure := range []bool{false, true} {
		name := "http"
		if secure {
			name = "connect"
		}
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusNoContent)
			})
			var target *httptest.Server
			if secure {
				target = httptest.NewTLSServer(handler)
			} else {
				target = httptest.NewServer(handler)
			}
			defer target.Close()

			dead, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			deadAddr := dead.Addr().String()
			dead.Close()

			p, err := OpenEgressProxy()
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			pu, err := url.Parse(p.Addr())
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			defer client.CloseIdleConnections()
			for _, enabled := range []bool{false, true} {
				if err := p.SetUpstream(fmt.Sprintf("http://%s", deadAddr), enabled); err != nil {
					t.Fatal(err)
				}
				res, err := client.Get(target.URL)
				if enabled {
					if err != nil || res.StatusCode != http.StatusNoContent {
						t.Fatalf("enabled: response=%v error=%v", res, err)
					}
					res.Body.Close()
				} else if err == nil && res.StatusCode == http.StatusNoContent {
					res.Body.Close()
					t.Fatal("disabled retry reached target")
				} else if err == nil {
					res.Body.Close()
				}
			}
			if got := hits.Load(); got != 1 {
				t.Fatalf("target hits=%d, want 1", got)
			}
		})
	}
}

func TestEgressProxyTargetFailureDoesNotFallback(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "target unreachable", http.StatusBadGateway)
	}))
	defer upstream.Close()
	p, err := OpenEgressProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.SetUpstream(upstream.URL, true); err != nil {
		t.Fatal(err)
	}
	client := egressTestClient(t, p)
	defer client.CloseIdleConnections()
	res, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadGateway || hits.Load() != 0 {
		t.Fatalf("status=%d direct hits=%d", res.StatusCode, hits.Load())
	}
}

func TestEgressProxyPostDoesNotDuplicate(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		if string(body) != "payload" {
			t.Errorf("body=%q", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer upstream.Close()
	p, err := OpenEgressProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.SetUpstream(upstream.URL, true); err != nil {
		t.Fatal(err)
	}
	client := egressTestClient(t, p)
	defer client.CloseIdleConnections()
	res, err := client.Post(target.URL, "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusProxyAuthRequired || hits.Load() != 0 {
		t.Fatalf("proxy auth response=%d direct hits=%d", res.StatusCode, hits.Load())
	}
}

func TestEgressProxyStripsProxyAuthorization(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Proxy-Authorization"); got != "" {
			t.Errorf("upstream credential leaked to target: %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := dead.Addr().String()
	dead.Close()
	p, err := OpenEgressProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.SetUpstream("http://user:secret@"+address, true); err != nil {
		t.Fatal(err)
	}
	client := egressTestClient(t, p)
	defer client.CloseIdleConnections()
	res, err := client.Get(target.URL)
	if err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("response=%v error=%v", res, err)
	}
	res.Body.Close()
}

func TestEgressProxySocks5FailureNoPanic(t *testing.T) {
	var hits atomic.Int32
	var attempts atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				attempts.Add(1)
				reader := bufio.NewReader(conn)
				greeting := make([]byte, 3)
				_, _ = io.ReadFull(reader, greeting)
				_, _ = conn.Write([]byte{5, 0})
				request := make([]byte, 4)
				_, _ = io.ReadFull(reader, request)
				if request[3] == 3 {
					length, _ := reader.ReadByte()
					_, _ = io.CopyN(io.Discard, reader, int64(length)+2)
				} else if request[3] == 1 {
					_, _ = io.CopyN(io.Discard, reader, 6)
				}
				_, _ = conn.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
			}()
		}
	}()
	p, err := OpenEgressProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.SetUpstream("socks5://"+ln.Addr().String(), true); err != nil {
		t.Fatal(err)
	}
	client := egressTestClient(t, p)
	defer client.CloseIdleConnections()
	res, err := client.Get(target.URL)
	if err == nil {
		res.Body.Close()
	}
	if hits.Load() != 0 || attempts.Load() != 1 {
		t.Fatalf("direct target hits=%d socks attempts=%d", hits.Load(), attempts.Load())
	}
}

func TestEgressProxySocks5ForwardsHealthyTarget(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var greeting [4]byte
		_, _ = io.ReadFull(conn, greeting[:])
		if greeting != [4]byte{5, 2, 0, 2} {
			t.Errorf("greeting=%v", greeting)
			return
		}
		_, _ = conn.Write([]byte{5, 2})
		var auth [11]byte
		_, _ = io.ReadFull(conn, auth[:])
		if string(auth[:]) != string([]byte{1, 4, 'u', 's', 'e', 'r', 4, 'p', 'a', 's', 's'}) {
			t.Errorf("SOCKS authentication payload differs")
			return
		}
		_, _ = conn.Write([]byte{1, 0})
		var request [10]byte
		_, _ = io.ReadFull(conn, request[:])
		wanted, _ := url.Parse(target.URL)
		if request[0] != 5 || request[1] != 1 || request[3] != 1 || net.IP(request[4:8]).String() != wanted.Hostname() {
			t.Errorf("SOCKS target=%v", request)
			return
		}
		upstream, err := net.Dial("tcp", wanted.Host)
		if err != nil {
			t.Errorf("dial target: %v", err)
			return
		}
		defer upstream.Close()
		_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		go io.Copy(upstream, conn)
		_, _ = io.Copy(conn, upstream)
	}()
	p, err := OpenEgressProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.SetUpstream("socks5://user:pass@"+ln.Addr().String(), false); err != nil {
		t.Fatal(err)
	}
	client := egressTestClient(t, p)
	defer client.CloseIdleConnections()
	res, err := client.Get(target.URL)
	if err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("response=%v error=%v", res, err)
	}
	res.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("target hits=%d", hits.Load())
	}
}

func TestEgressProxyUpstreamSchemes(t *testing.T) {
	for _, scheme := range []string{"http-auth", "https-tls-failure", "socks5-refused"} {
		t.Run(scheme, func(t *testing.T) {
			var hits atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer target.Close()
			var upstream string
			switch scheme {
			case "http-auth":
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusProxyAuthRequired) }))
				defer srv.Close()
				upstream = srv.URL
			case "https-tls-failure":
				srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
				defer srv.Close()
				upstream = srv.URL
			case "socks5-refused":
				dead, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				upstream = "socks5://" + dead.Addr().String()
				dead.Close()
			}
			p, err := OpenEgressProxy()
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if err := p.SetUpstream(upstream, true); err != nil {
				t.Fatal(err)
			}
			client := egressTestClient(t, p)
			defer client.CloseIdleConnections()
			res, err := client.Get(target.URL)
			if err != nil || res.StatusCode != http.StatusNoContent {
				t.Fatalf("response=%v error=%v", res, err)
			}
			res.Body.Close()
			if hits.Load() != 1 {
				t.Fatalf("direct hits=%d", hits.Load())
			}
		})
	}
}

func TestEgressProxyConfigSwapKeepsOpenTunnel(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	p, err := OpenEgressProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.SetUpstream("", true); err != nil {
		t.Fatal(err)
	}
	proxyURL, _ := url.Parse(p.Addr())
	client, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _ = fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target.Addr(), target.Addr())
	reader := bufio.NewReader(client)
	res, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT response=%v error=%v", res, err)
	}
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().String()
	dead.Close()
	if err := p.SetUpstream("http://"+deadAddr, false); err != nil {
		t.Fatal(err)
	}
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = client.Write([]byte("ping"))
	answer := make([]byte, 4)
	if _, err := io.ReadFull(reader, answer); err != nil || string(answer) != "ping" {
		t.Fatalf("existing tunnel response=%q error=%v", answer, err)
	}
}
