package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"syscall"
	"time"

	xproxy "golang.org/x/net/proxy"
)

const (
	// directDialTimeout bounds the TCP connect of a direct dial and of the
	// connection to a proxy.
	directDialTimeout = 10 * time.Second
	// defaultConnectTimeout bounds the tunnel set-up through a proxy when the
	// caller gives no WithConnectTimeout.
	defaultConnectTimeout = 30 * time.Second
)

type dialOptions struct {
	connectTimeout time.Duration
}

// Option configures Dialer and HostDialer.
type Option func(*dialOptions)

// WithConnectTimeout bounds the set-up of a tunnel through a proxy (the TLS
// handshake with an https proxy, the HTTP CONNECT exchange or the SOCKS5
// negotiation) when the context has no earlier deadline; the default is 30
// seconds. A value of zero or less keeps the default, so the set-up is never
// unbounded. It does not limit the life of the connection once it is up.
func WithConnectTimeout(d time.Duration) Option {
	return func(o *dialOptions) {
		if d > 0 {
			o.connectTimeout = d
		}
	}
}

func newDialOptions(opts []Option) dialOptions {
	o := dialOptions{connectTimeout: defaultConnectTimeout}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// control is net.Dialer.Control: it runs after name resolution, once per
// address the dialer is about to connect to.
func (p Policy) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrBlocked, address)
	}
	return p.Check(ap.Addr())
}

// DialFunc dials a TCP address.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Dialer returns a DialFunc that connects directly, or through proxy when it
// is not nil. Every address it connects to, the proxy's included, passes the
// Policy after resolution. With a proxy the destination must be an IP address
// (see checkTarget); HostDialer resolves a name first, for HTTP clients. The
// set-up of a tunnel is bounded by WithConnectTimeout.
func (p Policy) Dialer(proxy *url.URL, opts ...Option) (DialFunc, error) {
	o := newDialOptions(opts)
	d := &net.Dialer{Timeout: directDialTimeout, Control: p.control}
	if proxy == nil {
		return d.DialContext, nil
	}
	if proxy.User != nil {
		return nil, ErrProxyUserinfo
	}
	switch proxy.Scheme {
	case "socks5":
		// SOCKS5 fails only for a nil forward dialer, and a *net.Dialer is a
		// ContextDialer.
		sd, _ := xproxy.SOCKS5("tcp", proxyAddr(proxy), nil, d)
		cd, _ := sd.(xproxy.ContextDialer)
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			if err := p.checkTarget(addr); err != nil {
				return nil, err
			}
			// The negotiation honours the context's deadline and its
			// cancellation, and the deadline is not kept on the connection.
			ctx, cancel := context.WithTimeout(ctx, o.connectTimeout)
			defer cancel()
			return cd.DialContext(ctx, network, addr)
		}, nil
	case "http", "https":
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			if err := p.checkTarget(addr); err != nil {
				return nil, err
			}
			return connect(ctx, d, proxy, network, addr, o.connectTimeout)
		}, nil
	default:
		return nil, fmt.Errorf("%w: scheme %q", ErrProxy, proxy.Scheme)
	}
}

// checkTarget checks the destination handed to a proxied dial. It must be an
// IP address: a host name would be resolved by the proxy, where this policy
// cannot see the answer, so it fails closed. HostDialer resolves names itself
// and hands the dialer addresses. The port must be a number and the address must have no zone. Only the canonical notations netip accepts
// are addresses; 2130706433, 0x7f.1 and 0177.0.0.1 are host names here.
func (p Policy) checkTarget(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrBlocked, addr)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("%w: %q is not a port", ErrBlocked, port)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %q is not an IP address", ErrBlocked, host)
	}
	// A zone is not part of an address the proxy can be given (SOCKS5 would
	// send "addr%zone" as a name for the proxy to resolve).
	if ip.Zone() != "" {
		return fmt.Errorf("%w: %q has a zone", ErrBlocked, host)
	}
	return p.Check(ip)
}

// lookup resolves a host name; a variable so a test can answer.
var lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// HostDialer is Dialer for a client that hands the dialer a host name, as
// net/http does. Without a proxy it is Dialer: the dialer resolves the name
// and Control checks each address. With a proxy the name would be resolved by
// the proxy, where this Policy cannot see the answer, so it is resolved here,
// every address is checked (one blocked address refuses the name, so a record
// set that mixes a public and a blocked address cannot be used to slip
// through), and the proxy is asked for an address, never for the name.
func (p Policy) HostDialer(proxy *url.URL, opts ...Option) (DialFunc, error) {
	dial, err := p.Dialer(proxy, opts...)
	if err != nil || proxy == nil {
		return dial, err
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrBlocked, addr)
		}
		if _, err := netip.ParseAddr(host); err == nil {
			return dial(ctx, network, addr)
		}
		ips, err := lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("%w: %q does not resolve", ErrBlocked, host)
		}
		for _, ip := range ips {
			if err := p.Check(ip); err != nil {
				return nil, err
			}
		}
		var last error
		for _, ip := range ips {
			var c net.Conn
			if c, last = dial(ctx, network, net.JoinHostPort(ip.String(), port)); last == nil {
				return c, nil
			}
		}
		return nil, last
	}, nil
}

// proxyAddr is the host:port of the proxy: the URL's port, or the scheme's
// default (http 80, https 443, socks5 1080) when it names none.
func proxyAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443", "socks5": "1080"}[u.Scheme]
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// proxyTLSConfig is the TLS configuration for an https proxy; tests replace
// it to trust their proxy's certificate.
var proxyTLSConfig = func() *tls.Config { return &tls.Config{MinVersion: tls.VersionTLS12} }

// connect opens an HTTP CONNECT tunnel to addr through the proxy. The set-up
// has a deadline even when ctx has none: timeout, or ctx's own if earlier. The
// deadline is on the connection, so it bounds the TLS handshake and the
// CONNECT exchange; cancelling ctx expires it at once.
func connect(ctx context.Context, d *net.Dialer, proxy *url.URL, network, addr string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := d.DialContext(ctx, network, proxyAddr(proxy))
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline() // always set: WithTimeout
	_ = conn.SetDeadline(deadline)
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Unix(1, 0)) // in the past: unblock the set-up
		case <-done:
		}
	}()
	tunnel, br, err := handshake(ctx, conn, proxy, addr)
	close(done)
	<-exited
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %w", ErrProxy, err)
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		// The tunnel's first bytes arrived with the response (a server that
		// speaks first): hand them to the reader before the connection.
		return &bufferedConn{Conn: tunnel, r: br}, nil
	}
	return tunnel, nil
}

// handshake sets up the tunnel on a connection to the proxy: TLS for an https
// proxy, then CONNECT addr. It returns the connection to use and the reader
// that holds whatever followed the proxy's response.
func handshake(ctx context.Context, conn net.Conn, proxy *url.URL, addr string) (net.Conn, *bufio.Reader, error) {
	if proxy.Scheme == "https" {
		cfg := proxyTLSConfig()
		cfg.ServerName = proxy.Hostname()
		tc := tls.Client(conn, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, nil, err
		}
		conn = tc
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
	br := bufio.NewReader(conn)
	var resp *http.Response
	err := req.Write(conn)
	if err == nil {
		resp, err = http.ReadResponse(br, req)
	}
	if err != nil {
		return nil, nil, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("proxy answered %s", resp.Status)
	}
	return conn, br, nil
}

// bufferedConn reads what the CONNECT response's reader already holds first.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }
