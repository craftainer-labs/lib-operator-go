package egress

import (
	"errors"
	"fmt"
	"net/netip"
)

// EnvMode names the environment variable that selects the Mode.
const EnvMode = "EGRESS_MODE"

// Mode selects which destination ranges are blocked.
type Mode string

// Values of Mode.
const (
	// ModeStrict, the default, blocks the private ranges (RFC 1918, CGNAT,
	// ULA) as well as everything ModeAllowPrivate blocks.
	ModeStrict Mode = "strict"
	// ModeAllowPrivate blocks the special-purpose ranges that are never a
	// public or a cluster-internal destination (loopback, link-local, the
	// cloud metadata addresses and so on), for clusters whose backends are on
	// private addresses.
	ModeAllowPrivate Mode = "allow-private"
)

var (
	// ErrBlocked means the destination is in a blocked range.
	ErrBlocked = errors.New("destination is blocked by the egress policy")
	// ErrProxyUserinfo means the proxy URL carries user:password@.
	ErrProxyUserinfo = errors.New("proxy URL must not carry credentials")
	// ErrInvalidMode means EGRESS_MODE is not a Mode.
	ErrInvalidMode = errors.New("invalid egress mode")
	// ErrProxy means the proxy refused or could not set up the tunnel.
	ErrProxy = errors.New("proxy tunnel failed")
)

func prefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

var (
	// alwaysBlocked holds in both modes. Every entry is a row of the IANA
	// IPv4 or IPv6 Special-Purpose Address Registry
	// (https://www.iana.org/assignments/iana-ipv4-special-registry and
	// .../iana-ipv6-special-registry; RFC 6890 defines them), except where a
	// comment says otherwise. A row that is globally reachable (AS112, AMT,
	// the anycast addresses) is blocked too: none of them is a backend.
	alwaysBlocked = prefixes(
		// IPv4.
		"0.0.0.0/8",       // "This network", RFC 791 section 3.2 (holds 0.0.0.0/32, RFC 1122)
		"127.0.0.0/8",     // Loopback, RFC 1122 section 3.2.1.3
		"169.254.0.0/16",  // Link Local, RFC 3927 (holds 169.254.169.254, the cloud metadata address)
		"192.0.0.0/24",    // IETF Protocol Assignments, RFC 6890 section 2.1 (holds 192.0.0.192, Oracle's metadata address)
		"192.0.2.0/24",    // Documentation (TEST-NET-1), RFC 5737
		"192.31.196.0/24", // AS112-v4, RFC 7535
		"192.52.193.0/24", // AMT, RFC 7450
		"192.88.99.0/24",  // Deprecated 6to4 Relay Anycast, RFC 7526 (holds 192.88.99.2, 6a44-relay, RFC 6751)
		"192.175.48.0/24", // Direct Delegation AS112 Service, RFC 7534
		"198.18.0.0/15",   // Benchmarking, RFC 2544
		"198.51.100.0/24", // Documentation (TEST-NET-2), RFC 5737
		"203.0.113.0/24",  // Documentation (TEST-NET-3), RFC 5737
		"224.0.0.0/4",     // Multicast, RFC 5771 (the IANA multicast registry, not the special-purpose one)
		"240.0.0.0/4",     // Reserved, RFC 1112 section 4 (holds 255.255.255.255, Limited Broadcast, RFC 919)
		// IPv6.
		"::/96",          // Unspecified (::/128) and the deprecated IPv4-compatible addresses (::a.b.c.d, RFC 4291 section 2.5.5.1), and loopback (::1/128, RFC 4291)
		"100::/64",       // Discard-Only Address Block, RFC 6666
		"100:0:0:1::/64", // Dummy IPv6 Prefix, RFC 9780
		// IETF Protocol Assignments (2001::/23, RFC 2928) except Teredo
		// (2001::/32), which is checked through its embedded IPv4 address
		// below. The remaining /23 is the rows 2001:1::/32 (PCP, TURN and
		// DNS-SD SRP anycast, RFC 7723, RFC 8155, RFC 9665), 2001:2::/48
		// (Benchmarking, RFC 5180), 2001:3::/32 (AMT, RFC 7450), 2001:4:112::/48
		// (AS112-v6, RFC 7535), 2001:10::/28 (deprecated ORCHID, RFC 4843),
		// 2001:20::/28 (ORCHIDv2, RFC 7343) and 2001:30::/28 (Drone Remote ID,
		// RFC 9374), plus the unassigned rest, written as the CIDR blocks that
		// make up 2001::/23 minus 2001::/32.
		"2001:1::/32", "2001:2::/31", "2001:4::/30", "2001:8::/29", "2001:10::/28",
		"2001:20::/27", "2001:40::/26", "2001:80::/25", "2001:100::/24",
		"2001:db8::/32",     // Documentation, RFC 3849
		"2620:4f:8000::/48", // Direct Delegation AS112 Service, RFC 7534
		"3fff::/20",         // Documentation, RFC 9637
		"5f00::/16",         // Segment Routing (SRv6) SIDs, RFC 9602
		"fe80::/10",         // Link-Local Unicast, RFC 4291
		"fec0::/10",         // Site-local, deprecated, RFC 3879 (not in the registry)
		"ff00::/8",          // Multicast, RFC 4291 (the IANA multicast registry)
		// Cloud metadata addresses outside the ranges above.
		"100.100.100.200/32", "fd00:ec2::254/128", "168.63.129.16/32",
	)
	// privateBlocked is added in ModeStrict: the registry rows that are
	// forwardable but not globally reachable, which a cluster uses for its
	// own addresses.
	privateBlocked = prefixes(
		"10.0.0.0/8",     // Private-Use, RFC 1918
		"172.16.0.0/12",  // Private-Use, RFC 1918
		"192.168.0.0/16", // Private-Use, RFC 1918
		"100.64.0.0/10",  // Shared Address Space, RFC 6598
		"fc00::/7",       // Unique-Local, RFC 4193
	)
)

// Policy is the deny list of one Mode. The zero Policy blocks nothing; tests
// use it, the operator builds its Policy with NewPolicy.
type Policy struct {
	blocked []netip.Prefix
}

// ParseMode parses EGRESS_MODE: empty is ModeStrict.
func ParseMode(v string) (Mode, error) {
	switch Mode(v) {
	case "", ModeStrict:
		return ModeStrict, nil
	case ModeAllowPrivate:
		return ModeAllowPrivate, nil
	default:
		return "", fmt.Errorf("%w %q: allowed values are %q and %q", ErrInvalidMode, v, ModeStrict, ModeAllowPrivate)
	}
}

// NewPolicy returns the Policy of a Mode. Any Mode other than ModeAllowPrivate
// is ModeStrict.
func NewPolicy(m Mode) Policy {
	blocked := append([]netip.Prefix(nil), alwaysBlocked...)
	if m != ModeAllowPrivate {
		blocked = append(blocked, privateBlocked...)
	}
	return Policy{blocked: blocked}
}

// Check returns ErrBlocked when ip, or the IPv4 address it carries, is in a
// blocked range. The zero netip.Addr is blocked, and a zone identifier is
// ignored. An IPv6 address can carry an IPv4 one that a gateway then reaches:
// IPv4-mapped (::ffff:0:0/96), NAT64 (64:ff9b::/96 and 64:ff9b:1::/48), 6to4
// (2002::/16) and Teredo (2001::/32, the client address, inverted); the
// carried address gets the same IPv4 rules.
func (p Policy) Check(ip netip.Addr) error {
	if !ip.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrBlocked)
	}
	ip = ip.Unmap().WithZone("")
	if err := p.checkOne(ip); err != nil {
		return err
	}
	if v4, ok := embeddedIPv4(ip); ok {
		return p.checkOne(v4)
	}
	return nil
}

func (p Policy) checkOne(ip netip.Addr) error {
	for _, pre := range p.blocked {
		if pre.Contains(ip) {
			return fmt.Errorf("%w: %s", ErrBlocked, ip)
		}
	}
	return nil
}

var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")   // RFC 6052
	nat64Local     = netip.MustParsePrefix("64:ff9b:1::/48") // RFC 8215
	sixToFour      = netip.MustParsePrefix("2002::/16")      // RFC 3056
	teredo         = netip.MustParsePrefix("2001::/32")      // RFC 4380
)

// embeddedIPv4 returns the IPv4 address an IPv6 transition address carries.
func embeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() {
		return netip.Addr{}, false
	}
	b := ip.As16()
	switch {
	case nat64WellKnown.Contains(ip):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case nat64Local.Contains(ip):
		// RFC 6052 section 2.2, /48: the address skips the u octet (b[8]).
		return netip.AddrFrom4([4]byte{b[6], b[7], b[9], b[10]}), true
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	case teredo.Contains(ip):
		// RFC 4380 section 4: the client's address is stored inverted.
		return netip.AddrFrom4([4]byte{^b[12], ^b[13], ^b[14], ^b[15]}), true
	}
	return netip.Addr{}, false
}
