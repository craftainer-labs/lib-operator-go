// Package egress limits where an operator's backend connections may go (ADR
// 0002, "Egress policy"). A namespaced server kind lets a tenant aim the
// operator at any host, so every connection is checked after the host name has
// been resolved, at connect time, against a deny list chosen by the operator
// environment variable EGRESS_MODE. Connections may go through the server's
// proxy, which may not carry credentials.
//
// An operator parses the mode once at startup and hands the Policy to the code
// that builds its backend clients:
//
//	mode, err := egress.ParseMode(os.Getenv(egress.EnvMode))
//	if err != nil {
//		return err // errors.Is(err, egress.ErrInvalidMode)
//	}
//	policy := egress.NewPolicy(mode)
//
//	// Per server, with the proxy from server.Connection (nil for none):
//	dial, err := policy.HostDialer(proxyURL) // for net/http, which passes a host name
//	dial, err = policy.Dialer(proxyURL)      // for a client that passes an address
//
// # Modes
//
// ModeStrict (the default) blocks the private ranges (RFC 1918, CGNAT, ULA) as
// well as everything ModeAllowPrivate blocks: every address in the IANA IPv4
// and IPv6 Special-Purpose Address Registries that is not meant to be a public
// destination (loopback, unspecified, link-local, multicast, reserved,
// documentation, benchmarking, protocol assignments, discard, ORCHID, SRv6
// SIDs, the deprecated site-local and IPv4-compatible ranges) and the cloud
// metadata addresses. ModeAllowPrivate is for clusters whose backends are on
// private addresses.
//
// # Embedded IPv4
//
// An IPv6 address can carry an IPv4 one that a gateway then reaches, so the
// carried address is checked against the same IPv4 rules and a private or
// loopback IPv4 address cannot be smuggled through IPv6: IPv4-mapped
// (::ffff:0:0/96, RFC 4291), NAT64 (64:ff9b::/96, RFC 6052, and 64:ff9b:1::/48,
// RFC 8215), 6to4 (2002::/16, RFC 3056) and Teredo (2001::/32, RFC 4380, the
// client address, inverted). A NAT64 gateway that uses its own network-specific
// prefix (RFC 6052 section 2.1) cannot be recognised and is treated as the
// ordinary global IPv6 address it looks like. An IPv6 zone identifier is
// ignored: fe80::1%eth0 is the link-local address fe80::1.
//
// # Where the check runs
//
// Without a proxy the dialer resolves the name and the net.Dialer's Control
// hook checks each address it is about to connect to, so a DNS answer that
// changes between a check and the connection (DNS rebinding) is not trusted.
// With a proxy the name would be resolved by the proxy, where the Policy cannot
// see the answer: Dialer therefore refuses a host name (it fails closed) and
// HostDialer resolves the name itself, checks every address it returns (one
// blocked address refuses the name) and asks the proxy for an address, never
// for the name. The proxy's own address is checked after resolution too.
// Notations that are not canonical IP addresses (a decimal, octal or
// hexadecimal IPv4 such as 2130706433 or 0x7f.1) are not parsed as IP
// addresses: with a proxy they fail closed as host names, and without one the
// resolver's answer is what Control checks.
//
// # Deadlines
//
// Setting up the tunnel through a proxy (the TLS handshake with an https
// proxy, the HTTP CONNECT exchange, the SOCKS5 negotiation) always has a
// deadline, even when the context has none: WithConnectTimeout, 30 seconds by
// default. A context deadline that is earlier still wins, and cancelling the
// context abandons the setup. The deadline ends when the tunnel is up; it is
// not a limit on the connection's lifetime.
package egress
