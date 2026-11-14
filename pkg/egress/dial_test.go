package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestControl(t *testing.T) {
	p := NewPolicy(ModeStrict)
	for _, addr := range []string{"127.0.0.1:5432", "[::1]:5432", "[fe80::1%eth0]:5432", "[::ffff:10.1.2.3]:5432", "[64:ff9b::7f00:1]:5432", "0.0.0.0:1"} {
		if err := p.control("tcp", addr, nil); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: %v", addr, err)
		}
	}
	for _, addr := range []string{"8.8.8.8:5432", "[2606:4700::1111]:443", "[2606:4700::1111%eth0]:443"} {
		if err := p.control("tcp", addr, nil); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	// What is not an IP address and port fails closed.
	for _, addr := range []string{"not-an-address", "localhost:80", "2130706433:80", "0x7f.1:80", "0177.0.0.1:80", "127.1:80", "/run/x.sock", ""} {
		if err := p.control("tcp", addr, nil); !errors.Is(err, ErrBlocked) {
			t.Errorf("%q: %v", addr, err)
		}
	}
}

// A real dial to a loopback listener is stopped by the Control hook after the
// name has been resolved ("localhost"), before any packet is sent.
func TestDirectDialIsCheckedAfterResolution(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(l.Addr().String())

	blocked, err := NewPolicy(ModeStrict).Dialer(nil)
	if err != nil {
		t.Fatal(err)
	}
	// The loopback address in every spelling a client might pass: the check is
	// on the address the resolver produced, not on the text.
	for _, host := range []string{"localhost", "127.0.0.1", "127.1", "2130706433", "0x7f.0.0.1", "0177.0.0.1", "[::1]", "[::ffff:127.0.0.1]", "[::ffff:7f00:1]", "0.0.0.0", "[::]"} {
		if c, err := blocked(context.Background(), "tcp", host+":"+port); err == nil {
			_ = c.Close()
			t.Errorf("dial %s: connected", host)
		}
	}
	if _, err := blocked(context.Background(), "tcp", "localhost:"+port); !errors.Is(err, ErrBlocked) {
		t.Errorf("dial localhost: %v", err)
	}
	if _, err := blocked(context.Background(), "unix", "/run/does-not-exist.sock"); !errors.Is(err, ErrBlocked) {
		t.Errorf("dial unix: %v", err)
	}
	if n := accepted.Load(); n != 0 {
		t.Errorf("the listener accepted %d connections", n)
	}

	open, err := Policy{}.Dialer(nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := open(context.Background(), "tcp", l.Addr().String())
	if err != nil {
		t.Fatalf("dial with the zero Policy: %v", err)
	}
	_ = c.Close()
}

func TestProxyUserinfoAndScheme(t *testing.T) {
	for _, raw := range []string{"http://user:pw@proxy:3128", "socks5://user@proxy:1080", "https://:pw@proxy"} {
		u, _ := url.Parse(raw)
		if _, err := (Policy{}).Dialer(u); !errors.Is(err, ErrProxyUserinfo) {
			t.Errorf("%s: %v", raw, err)
		}
	}
	u, _ := url.Parse("ftp://proxy:21")
	if _, err := (Policy{}).Dialer(u); !errors.Is(err, ErrProxy) {
		t.Errorf("ftp: %v", err)
	}
}

func TestCheckTarget(t *testing.T) {
	p := NewPolicy(ModeStrict)
	for _, addr := range []string{
		"pg.example.com:5432", // a name fails closed: the proxy would resolve it
		"10.1.2.3:5432", "[fe80::1%eth0]:5432", "[::ffff:127.0.0.1]:80", "[64:ff9b::a9fe:a9fe]:80", "[2002:a9fe:a9fe::]:80",
		"no-port", "",
		// Notations that are not canonical IP addresses are names here.
		"2130706433:80", "0x7f000001:80", "0x7f.1:80", "0177.0.0.1:80", "127.1:80", "017700000001:80", "127.0.0.1.:80", "[::1%]:80",
	} {
		if err := p.checkTarget(addr); !errors.Is(err, ErrBlocked) {
			t.Errorf("%q: %v", addr, err)
		}
	}
	// A zone and a port that is not a number are refused before the proxy is
	// told about them.
	for _, addr := range []string{"[2606:4700::1111%eth0]:443", "8.8.8.8:abc", "8.8.8.8:99999", "8.8.8.8:-1", "8.8.8.8:", "8.8.8.8:80\r\nHost: x"} {
		if err := p.checkTarget(addr); !errors.Is(err, ErrBlocked) {
			t.Errorf("%q: %v", addr, err)
		}
	}
	if err := p.checkTarget("[2606:4700::1111]:443"); err != nil {
		t.Errorf("public address: %v", err)
	}
}

// proxyServer answers CONNECT with status and, on 200, echoes. It counts the
// requests it received.
func proxyServer(t *testing.T, status int, useTLS bool) (*httptest.Server, *url.URL, *atomic.Int32) {
	t.Helper()
	hits := new(atomic.Int32)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodConnect || r.Host != "10.9.9.9:5432" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		if status != http.StatusOK {
			http.Error(w, "no", status)
			return
		}
		hj := w.(http.Hijacker)
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(conn, conn)
	})
	var srv *httptest.Server
	if useTLS {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return srv, u, hits
}

// rawProxy listens on a loopback port and hands each connection to serve; it
// returns the address. Connections are closed when the test ends.
func rawProxy(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			conns = append(conns, c)
			go serve(c)
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		<-done
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return l.Addr().String()
}

func echo(t *testing.T, c net.Conn) {
	t.Helper()
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v", buf, err)
	}
}

func TestHTTPProxyTunnel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, u, hits := proxyServer(t, http.StatusOK, false)
	dial, err := Policy{}.Dialer(u)
	if err != nil {
		t.Fatal(err)
	}
	c, err := dial(ctx, "tcp", "10.9.9.9:5432")
	if err != nil {
		t.Fatal(err)
	}
	echo(t, c)

	// A refusal is an error.
	_, refuse, _ := proxyServer(t, http.StatusForbidden, false)
	dial, _ = Policy{}.Dialer(refuse)
	if _, err := dial(ctx, "tcp", "10.9.9.9:5432"); !errors.Is(err, ErrProxy) {
		t.Errorf("403: %v", err)
	}

	// A destination the policy blocks never reaches the proxy, in whatever
	// form it is spelled: CONNECT to it is never sent.
	before := hits.Load()
	strict, _ := NewPolicy(ModeStrict).Dialer(u)
	for _, dst := range []string{
		"10.9.9.9:5432", "127.0.0.1:80", "169.254.169.254:80", "[::1]:80", "[::ffff:169.254.169.254]:80", "[fe80::1%eth0]:80",
		"[64:ff9b::a9fe:a9fe]:80", "169.254.169.254.nip.io:80", "2130706433:80", "0x7f.1:80", "no-port",
	} {
		if _, err := strict(ctx, "tcp", dst); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: %v", dst, err)
		}
	}
	// The proxy itself is checked after resolution too (it is on loopback).
	if _, err := strict(ctx, "tcp", "8.8.8.8:5432"); !errors.Is(err, ErrBlocked) {
		t.Errorf("a blocked proxy was dialled: %v", err)
	}
	if n := hits.Load(); n != before {
		t.Errorf("the proxy received %d requests for blocked destinations", n-before)
	}
}

func TestHTTPSProxyTunnel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, u, _ := proxyServer(t, http.StatusOK, true)
	u.Host = "127.0.0.1:" + u.Port()

	old := proxyTLSConfig
	t.Cleanup(func() { proxyTLSConfig = old })
	proxyTLSConfig = func() *tls.Config {
		pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
		return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	}
	dial, err := Policy{}.Dialer(u)
	if err != nil {
		t.Fatal(err)
	}
	c, err := dial(ctx, "tcp", "10.9.9.9:5432")
	if err != nil {
		t.Fatal(err)
	}
	echo(t, c)

	// A proxy that does not speak TLS fails the handshake.
	plain, _, _ := proxyServer(t, http.StatusOK, false)
	pu, _ := url.Parse(plain.URL)
	pu.Scheme = "https"
	dial, _ = Policy{}.Dialer(pu)
	if _, err := dial(ctx, "tcp", "10.9.9.9:5432"); !errors.Is(err, ErrProxy) {
		t.Errorf("plain proxy over https: %v", err)
	}
}

// A proxy that is down is a dial error; one that hangs up is a proxy error.
func TestProxyFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	hangup, _ := url.Parse("http://" + addr)
	dial, _ := Policy{}.Dialer(hangup)
	if _, err := dial(ctx, "tcp", "10.9.9.9:5432"); !errors.Is(err, ErrProxy) {
		t.Errorf("hang-up: %v", err)
	}
	_ = l.Close()

	for _, scheme := range []string{"http", "socks5"} {
		down, _ := url.Parse(scheme + "://" + addr)
		dial, _ = Policy{}.Dialer(down)
		if _, err := dial(ctx, "tcp", "10.9.9.9:5432"); err == nil {
			t.Errorf("%s proxy that is down: no error", scheme)
		}
	}
}

// The CONNECT exchange has a deadline even when the context has none, and an
// earlier context deadline or a cancellation ends it sooner.
func TestConnectHasADeadline(t *testing.T) {
	silent := rawProxy(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	for _, scheme := range []string{"http", "https", "socks5"} {
		u, _ := url.Parse(scheme + "://" + silent)
		within := func(ctx context.Context, name string, d time.Duration, opts ...Option) {
			t.Helper()
			dial, err := Policy{}.Dialer(u, opts...)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			c, err := dial(ctx, "tcp", "10.9.9.9:5432")
			if err == nil {
				_ = c.Close()
				t.Fatalf("%s %s: connected through a proxy that never answers", scheme, name)
			}
			if scheme != "socks5" && !errors.Is(err, ErrProxy) {
				t.Errorf("%s %s: %v", scheme, name, err)
			}
			if el := time.Since(start); el > d {
				t.Errorf("%s %s: took %v, want at most %v: %v", scheme, name, el, d, err)
			}
		}
		// No deadline in the context: the option bounds it.
		within(context.Background(), "option", 5*time.Second, WithConnectTimeout(150*time.Millisecond))
		// A context deadline that is earlier than the option wins.
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		within(ctx, "context deadline", 5*time.Second, WithConnectTimeout(time.Minute))
		cancel()
		// Cancelling abandons the set-up at once.
		ctx, cancel = context.WithCancel(context.Background())
		time.AfterFunc(150*time.Millisecond, cancel)
		within(ctx, "cancellation", 5*time.Second, WithConnectTimeout(time.Minute))
		cancel()
	}
}

func TestConnectTimeoutOption(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		if o := newDialOptions([]Option{WithConnectTimeout(d)}); o.connectTimeout != defaultConnectTimeout {
			t.Errorf("WithConnectTimeout(%v) = %v, want the default %v", d, o.connectTimeout, defaultConnectTimeout)
		}
	}
	if o := newDialOptions(nil); o.connectTimeout != 30*time.Second {
		t.Errorf("default = %v, want 30s", o.connectTimeout)
	}
	if o := newDialOptions([]Option{WithConnectTimeout(time.Second)}); o.connectTimeout != time.Second {
		t.Errorf("WithConnectTimeout(1s) = %v", o.connectTimeout)
	}
}

// The set-up deadline ends with the set-up: a tunnel that outlives it, and a
// context that is cancelled once the dial returned, leave the connection alone.
func TestTunnelOutlivesTheSetupDeadline(t *testing.T) {
	_, u, _ := proxyServer(t, http.StatusOK, false)
	socks := rawProxy(t, serveSOCKS5)
	su, _ := url.Parse("socks5://" + socks)
	for _, pu := range []*url.URL{u, su} {
		dial, err := Policy{}.Dialer(pu, WithConnectTimeout(300*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		c, err := dial(ctx, "tcp", "10.9.9.9:5432")
		if err != nil {
			t.Fatalf("%s: %v", pu.Scheme, err)
		}
		cancel()
		time.Sleep(600 * time.Millisecond)
		echo(t, c)
	}
}

// serveSOCKS5 is a SOCKS5 server without authentication that echoes.
func serveSOCKS5(c net.Conn) {
	greeting := make([]byte, 3)
	req := make([]byte, 10)
	if _, err := io.ReadFull(c, greeting); err != nil {
		return
	}
	_, _ = c.Write([]byte{5, 0})
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	_, _ = io.Copy(c, c)
}

func TestSOCKS5ChecksTheDestination(t *testing.T) {
	addr := rawProxy(t, serveSOCKS5)
	u, _ := url.Parse("socks5://" + addr)
	dial, err := Policy{}.Dialer(u)
	if err != nil {
		t.Fatal(err)
	}
	c, err := dial(context.Background(), "tcp", "10.9.9.9:5432")
	if err != nil {
		t.Fatal(err)
	}
	echo(t, c)

	strict, err := NewPolicy(ModeStrict).Dialer(u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strict(context.Background(), "tcp", "10.9.9.9:5432"); !errors.Is(err, ErrBlocked) {
		t.Errorf("private destination: %v", err)
	}
	if _, err := strict(context.Background(), "tcp", "pg.example.com:5432"); !errors.Is(err, ErrBlocked) {
		t.Errorf("a name must not reach the proxy: %v", err)
	}
}

// A server that speaks first can send its first bytes in the same segment as
// the proxy's response: they are not lost.
func TestTunnelKeepsBytesThatFollowTheResponse(t *testing.T) {
	addr := rawProxy(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\r\n" {
				break
			}
		}
		_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\nhello"))
		_, _ = io.Copy(c, br)
	})
	u, _ := url.Parse("http://" + addr)
	dial, _ := Policy{}.Dialer(u)
	c, err := dial(context.Background(), "tcp", "10.9.9.9:5432")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("first bytes = %q, %v", buf, err)
	}
	echo(t, c)
}

func TestProxyAddrDefaultPorts(t *testing.T) {
	for raw, want := range map[string]string{
		"http://proxy.corp": "proxy.corp:80", "https://proxy.corp": "proxy.corp:443", "socks5://proxy.corp": "proxy.corp:1080",
		"http://proxy.corp:3128": "proxy.corp:3128", "socks5://[2001:db8::1]": "[2001:db8::1]:1080",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := proxyAddr(u); got != want {
			t.Errorf("proxyAddr(%s) = %s, want %s", raw, got, want)
		}
	}
}

// stubLookup replaces the resolver for one test and counts its calls.
func stubLookup(t *testing.T, answer func(host string) ([]netip.Addr, error)) *atomic.Int32 {
	t.Helper()
	calls := new(atomic.Int32)
	old := lookup
	t.Cleanup(func() { lookup = old })
	lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		calls.Add(1)
		return answer(host)
	}
	return calls
}

func TestHostDialerWithoutAProxyIsTheDialer(t *testing.T) {
	dial, err := NewPolicy(ModeStrict).HostDialer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dial(context.Background(), "tcp", "localhost:1"); !errors.Is(err, ErrBlocked) {
		t.Errorf("loopback: %v", err)
	}
	u, _ := url.Parse("http://user:pw@proxy:3128")
	if _, err := (Policy{}).HostDialer(u); !errors.Is(err, ErrProxyUserinfo) {
		t.Errorf("userinfo: %v", err)
	}
}

// With a proxy a host name is resolved here, every address is checked, and the
// proxy is asked for an address, never for the name.
func TestHostDialerResolvesBeforeTheProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, u, hits := proxyServer(t, http.StatusOK, false)
	addrs := map[string][]netip.Addr{
		"store.example.test":  {netip.MustParseAddr("10.9.9.9")},
		"second.example.test": {netip.MustParseAddr("10.9.9.8"), netip.MustParseAddr("10.9.9.9")},
		"mixed.example.test":  {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254")},
		"meta.example.test":   {netip.MustParseAddr("::ffff:169.254.169.254")},
		"nat64.example.test":  {netip.MustParseAddr("64:ff9b::7f00:1")},
		"zone.example.test":   {netip.MustParseAddr("fe80::1%eth0")},
		"none.example.test":   {},
		"down.example.test":   {netip.MustParseAddr("10.9.9.8")},
		// The resolver (cgo getaddrinfo) can turn these into loopback.
		"2130706433": {netip.MustParseAddr("127.0.0.1")},
		"0x7f.1":     {netip.MustParseAddr("127.0.0.1")},
	}
	boom := errors.New("no such host")
	calls := stubLookup(t, func(host string) ([]netip.Addr, error) {
		if a, ok := addrs[host]; ok {
			return a, nil
		}
		return nil, boom
	})

	dial, err := Policy{}.HostDialer(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"store.example.test", "second.example.test"} {
		c, err := dial(ctx, "tcp", host+":5432")
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		echo(t, c)
	}
	// The answer is resolved once and the proxy gets the address: a second
	// resolution (DNS rebinding) cannot change where the connection goes.
	if n := calls.Load(); n != 2 {
		t.Errorf("lookups = %d, want 1 per name", n)
	}
	// An IP literal goes straight to the proxy path.
	c, err := dial(ctx, "tcp", "10.9.9.9:5432")
	if err != nil {
		t.Fatal(err)
	}
	echo(t, c)
	// The proxy answered 400 for the only address: its error is returned.
	if _, err := dial(ctx, "tcp", "down.example.test:5432"); !errors.Is(err, ErrProxy) {
		t.Errorf("only address refused: %v", err)
	}
	if _, err := dial(ctx, "tcp", "absent.example.test:5432"); !errors.Is(err, boom) {
		t.Errorf("resolver error: %v", err)
	}
	if _, err := dial(ctx, "tcp", "none.example.test:5432"); !errors.Is(err, ErrBlocked) {
		t.Errorf("no address: %v", err)
	}
	if _, err := dial(ctx, "tcp", "no-port"); !errors.Is(err, ErrBlocked) {
		t.Errorf("no port: %v", err)
	}

	strict, err := NewPolicy(ModeStrict).HostDialer(u)
	if err != nil {
		t.Fatal(err)
	}
	before := hits.Load()
	for _, host := range []string{
		"store.example.test", "mixed.example.test", "meta.example.test", "nat64.example.test", "zone.example.test", "2130706433", "0x7f.1",
	} {
		if _, err := strict(ctx, "tcp", host+":5432"); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s under strict: %v", host, err)
		}
	}
	if n := hits.Load(); n != before {
		t.Errorf("the proxy received %d requests for blocked destinations", n-before)
	}
}

func TestDefaultLookupResolves(t *testing.T) {
	ips, err := lookup(context.Background(), "localhost")
	if err != nil || len(ips) == 0 {
		t.Errorf("lookup(localhost) = %v, %v", ips, err)
	}
}
