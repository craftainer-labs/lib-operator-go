package egress

import (
	"errors"
	"net/netip"
	"testing"
)

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeStrict, "strict": ModeStrict, "allow-private": ModeAllowPrivate} {
		got, err := ParseMode(in)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"off", "Strict", " strict", "allow_private"} {
		if _, err := ParseMode(bad); !errors.Is(err, ErrInvalidMode) {
			t.Errorf("ParseMode(%q) = %v", bad, err)
		}
	}
}

// lastAddr is the last address of a prefix.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

// registryRow is a row of an IANA Special-Purpose Address Registry, or of the
// IANA multicast registries, with the RFC that defines it.
type registryRow struct {
	cidr string
	name string
	rfc  string
}

// Rows blocked in both modes. The IPv4 rows are from
// https://www.iana.org/assignments/iana-ipv4-special-registry and the IPv6
// rows from https://www.iana.org/assignments/iana-ipv6-special-registry,
// fetched 2026-11-14.
var blockedRows = []registryRow{
	// IPv4 Special-Purpose Address Registry.
	{"0.0.0.0/8", `"This network"`, "RFC 791 section 3.2"},
	{"0.0.0.0/32", `"This host on this network"`, "RFC 1122 section 3.2.1.3"},
	{"127.0.0.0/8", "Loopback", "RFC 1122 section 3.2.1.3"},
	{"169.254.0.0/16", "Link Local", "RFC 3927"},
	{"169.254.169.254/32", "cloud metadata address, in Link Local", "RFC 3927"},
	{"192.0.0.0/24", "IETF Protocol Assignments", "RFC 6890 section 2.1"},
	{"192.0.0.0/29", "IPv4 Service Continuity Prefix", "RFC 7335"},
	{"192.0.0.8/32", "IPv4 dummy address", "RFC 7600"},
	{"192.0.0.9/32", "Port Control Protocol Anycast", "RFC 7723"},
	{"192.0.0.10/32", "Traversal Using Relays around NAT Anycast", "RFC 8155"},
	{"192.0.0.170/32", "NAT64/DNS64 Discovery", "RFC 8880, RFC 7050 section 2.2"},
	{"192.0.0.171/32", "NAT64/DNS64 Discovery", "RFC 8880, RFC 7050 section 2.2"},
	{"192.0.0.192/32", "Oracle metadata address, in IETF Protocol Assignments", "RFC 6890 section 2.1"},
	{"192.0.2.0/24", "Documentation (TEST-NET-1)", "RFC 5737"},
	{"192.31.196.0/24", "AS112-v4", "RFC 7535"},
	{"192.52.193.0/24", "AMT", "RFC 7450"},
	{"192.88.99.0/24", "Deprecated (6to4 Relay Anycast)", "RFC 7526"},
	{"192.88.99.2/32", "6a44-relay anycast address", "RFC 6751"},
	{"192.175.48.0/24", "Direct Delegation AS112 Service", "RFC 7534"},
	{"198.18.0.0/15", "Benchmarking", "RFC 2544"},
	{"198.51.100.0/24", "Documentation (TEST-NET-2)", "RFC 5737"},
	{"203.0.113.0/24", "Documentation (TEST-NET-3)", "RFC 5737"},
	{"240.0.0.0/4", "Reserved", "RFC 1112 section 4"},
	{"255.255.255.255/32", "Limited Broadcast", "RFC 8190, RFC 919 section 7"},
	{"100.100.100.200/32", "Alibaba Cloud metadata address", "not in the registry"},
	{"168.63.129.16/32", "Azure wire server", "not in the registry"},
	// IANA IPv4 Multicast Address Space Registry.
	{"224.0.0.0/4", "Multicast", "RFC 5771"},
	// IPv6 Special-Purpose Address Registry.
	{"::1/128", "Loopback Address", "RFC 4291"},
	{"::/128", "Unspecified Address", "RFC 4291"},
	{"::/96", "IPv4-compatible Address (deprecated)", "RFC 4291 section 2.5.5.1"},
	{"::ffff:0:0:0/96", "IPv4-translated (SIIT) address", "RFC 2765, RFC 6145; not in the registry"},
	{"100::/64", "Discard-Only Address Block", "RFC 6666"},
	{"100:0:0:1::/64", "Dummy IPv6 Prefix", "RFC 9780"},
	// 2001::/23 is "IETF Protocol Assignments" (RFC 2928) and is not
	// forwardable as a whole; the policy blocks all of it but Teredo
	// (2001::/32, RFC 4380), which is checked through the IPv4 address it
	// carries. These nine blocks are 2001::/23 minus 2001::/32.
	{"2001:1::/32", "IETF Protocol Assignments", "RFC 2928"},
	{"2001:2::/31", "IETF Protocol Assignments", "RFC 2928"},
	{"2001:4::/30", "IETF Protocol Assignments", "RFC 2928"},
	{"2001:8::/29", "IETF Protocol Assignments", "RFC 2928"},
	{"2001:10::/28", "IETF Protocol Assignments", "RFC 2928"},
	{"2001:20::/27", "IETF Protocol Assignments", "RFC 2928"},
	{"2001:40::/26", "IETF Protocol Assignments", "RFC 2928"},
	{"2001:80::/25", "IETF Protocol Assignments", "RFC 2928"},
	{"2001:100::/24", "IETF Protocol Assignments", "RFC 2928"},
	{"2001:1::1/128", "Port Control Protocol Anycast", "RFC 7723"},
	{"2001:1::2/128", "Traversal Using Relays around NAT Anycast", "RFC 8155"},
	{"2001:1::3/128", "DNS-SD Service Registration Protocol Anycast", "RFC 9665"},
	{"2001:2::/48", "Benchmarking", "RFC 5180"},
	{"2001:3::/32", "AMT", "RFC 7450"},
	{"2001:4:112::/48", "AS112-v6", "RFC 7535"},
	{"2001:10::/28", "Deprecated (previously ORCHID)", "RFC 4843"},
	{"2001:20::/28", "ORCHIDv2", "RFC 7343"},
	{"2001:30::/28", "Drone Remote ID Protocol Entity Tags (DETs) Prefix", "RFC 9374"},
	{"2001:db8::/32", "Documentation", "RFC 3849"},
	{"2620:4f:8000::/48", "Direct Delegation AS112 Service", "RFC 7534"},
	{"3fff::/20", "Documentation", "RFC 9637"},
	{"5f00::/16", "Segment Routing (SRv6) SIDs", "RFC 9602"},
	{"fe80::/10", "Link-Local Unicast", "RFC 4291"},
	{"fd00:ec2::254/128", "AWS metadata address, in Unique-Local", "not in the registry"},
	{"fd20:ce::254/128", "GCP metadata address, in Unique-Local", "not in the registry"},
	// Not in the special-purpose registry.
	{"fec0::/10", "Site-local (deprecated)", "RFC 3879"},
	{"ff00::/8", "Multicast", "RFC 4291 section 2.7"},
}

// Rows blocked in ModeStrict only: forwardable, not globally reachable.
var privateRows = []registryRow{
	{"10.0.0.0/8", "Private-Use", "RFC 1918"},
	{"100.64.0.0/10", "Shared Address Space", "RFC 6598"},
	{"172.16.0.0/12", "Private-Use", "RFC 1918"},
	{"192.168.0.0/16", "Private-Use", "RFC 1918"},
	{"fc00::/7", "Unique-Local", "RFC 4193"},
}

// A row's first and last address are blocked, and the addresses just outside
// the row, unless another row covers them, are not.
func TestPolicyCheckRegistryRows(t *testing.T) {
	strict, allow := NewPolicy(ModeStrict), NewPolicy(ModeAllowPrivate)
	covered := func(a netip.Addr, rows ...[]registryRow) bool {
		for _, rs := range rows {
			for _, r := range rs {
				if netip.MustParsePrefix(r.cidr).Contains(a) {
					return true
				}
			}
		}
		return false
	}
	for _, r := range blockedRows {
		p := netip.MustParsePrefix(r.cidr)
		for _, a := range []netip.Addr{p.Masked().Addr(), lastAddr(p)} {
			for name, pol := range map[string]Policy{"strict": strict, "allow-private": allow} {
				if err := pol.Check(a); !errors.Is(err, ErrBlocked) {
					t.Errorf("%s: %s (%s, %s, %s) is not blocked: %v", name, a, r.cidr, r.name, r.rfc, err)
				}
			}
		}
		// The neighbours, unless a row covers them, they are Teredo addresses
		// (judged by the address they carry) or there is none.
		for _, n := range []netip.Addr{p.Masked().Addr().Prev(), lastAddr(p).Next()} {
			if !n.IsValid() || covered(n, blockedRows, privateRows) || teredo.Contains(n) {
				continue
			}
			if err := strict.Check(n); err != nil {
				t.Errorf("%s, next to %s (%s, %s), is blocked: %v", n, r.cidr, r.name, r.rfc, err)
			}
		}
	}
	for _, r := range privateRows {
		p := netip.MustParsePrefix(r.cidr)
		for _, a := range []netip.Addr{p.Masked().Addr(), lastAddr(p)} {
			if err := strict.Check(a); !errors.Is(err, ErrBlocked) {
				t.Errorf("strict: %s (%s, %s, %s) is not blocked: %v", a, r.cidr, r.name, r.rfc, err)
			}
			if err := allow.Check(a); err != nil {
				t.Errorf("allow-private: %s (%s, %s, %s) is blocked: %v", a, r.cidr, r.name, r.rfc, err)
			}
		}
	}
}

// Global addresses next to the rows, and rows the policy leaves open on
// purpose, are allowed in both modes.
func TestPolicyCheckPublicAddresses(t *testing.T) {
	public := []string{
		"8.8.8.8", "1.1.1.1", "11.0.0.1", "126.255.255.255", "128.0.0.1",
		"169.253.255.255", "169.255.0.1", "172.15.255.255", "172.32.0.1", "192.167.255.255", "192.169.0.1",
		"100.63.255.255", "100.128.0.1", "192.0.1.1", "192.0.3.1", "198.17.255.255", "198.20.0.0", "198.51.101.1",
		"203.0.112.255", "203.0.114.1", "192.88.98.255", "192.88.100.1", "223.255.255.255",
		"2606:4700::1111", "2001:4860:4860::8888", "2001:200::1", "2000::1", "3ffe::1", "3fff:1000::1",
		"5f01::1", "5eff:ffff::1", "100:0:0:2::1", "2620:4f:8001::1", "2a00:1450::1",
	}
	for _, s := range public {
		ip := netip.MustParseAddr(s)
		for name, pol := range map[string]Policy{"strict": NewPolicy(ModeStrict), "allow-private": NewPolicy(ModeAllowPrivate)} {
			if err := pol.Check(ip); err != nil {
				t.Errorf("%s: %s is blocked: %v", name, s, err)
			}
		}
	}
	if err := (Policy{}).Check(netip.MustParseAddr("127.0.0.1")); err != nil {
		t.Errorf("the zero Policy blocks: %v", err)
	}
	if err := NewPolicy("anything else").Check(netip.MustParseAddr("10.0.0.1")); !errors.Is(err, ErrBlocked) {
		t.Errorf("an unknown Mode is not strict: %v", err)
	}
}

// v4 is the 4 bytes of an IPv4 address.
func v4(s string) [4]byte { return netip.MustParseAddr(s).As4() }

// The IPv6 forms that carry an IPv4 address, as the RFCs lay them out.
var carriers = map[string]func(a [4]byte) netip.Addr{
	// RFC 4291 section 2.5.5.2: ::ffff:a.b.c.d.
	"IPv4-mapped": func(a [4]byte) netip.Addr { return netip.AddrFrom16(netip.AddrFrom4(a).As16()) },
	// RFC 6052 section 2.4: 64:ff9b::/96, the address in the last 32 bits.
	"NAT64 64:ff9b::/96": func(a [4]byte) netip.Addr {
		return netip.AddrFrom16([16]byte{0, 0x64, 0xff, 0x9b, 12: a[0], 13: a[1], 14: a[2], 15: a[3]})
	},
	// RFC 8215 with RFC 6052 section 2.2, /48: the address is split around
	// the reserved octet 8.
	"NAT64 64:ff9b:1::/48": func(a [4]byte) netip.Addr {
		return netip.AddrFrom16([16]byte{0, 0x64, 0xff, 0x9b, 0, 1, a[0], a[1], 0, a[2], a[3], 11: 0xaa, 15: 1})
	},
	// RFC 3056 section 2: 2002:V4ADDR::/48.
	"6to4": func(a [4]byte) netip.Addr {
		return netip.AddrFrom16([16]byte{0x20, 0x02, a[0], a[1], a[2], a[3], 15: 1})
	},
	// RFC 4380 section 4: 2001:0:server:flags:port:client, the port and the
	// client's address inverted.
	"Teredo": func(a [4]byte) netip.Addr {
		return netip.AddrFrom16([16]byte{0x20, 0x01, 0, 0, 1, 2, 3, 4, 0x80, 0, ^byte(0x12), ^byte(0x34), ^a[0], ^a[1], ^a[2], ^a[3]})
	},
}

// An IPv4 address carried by an IPv6 one gets the IPv4 rules, so a blocked
// IPv4 address cannot be smuggled through IPv6.
func TestPolicyCheckEmbeddedIPv4(t *testing.T) {
	always := []string{"127.0.0.1", "127.255.255.254", "169.254.169.254", "0.0.0.0", "0.1.2.3", "224.0.0.1", "255.255.255.255",
		"192.0.0.192", "192.0.2.1", "198.18.0.1", "100.100.100.200", "168.63.129.16", "240.0.0.1"}
	private := []string{"10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", "100.64.0.1"}
	public := []string{"8.8.8.8", "1.1.1.1", "172.15.0.1", "100.63.0.1", "93.184.216.34"}
	strict, allow := NewPolicy(ModeStrict), NewPolicy(ModeAllowPrivate)
	for kind, make6 := range carriers {
		for _, s := range always {
			for name, pol := range map[string]Policy{"strict": strict, "allow-private": allow} {
				if err := pol.Check(make6(v4(s))); !errors.Is(err, ErrBlocked) {
					t.Errorf("%s: %s carrying %s is not blocked: %v", name, kind, s, err)
				}
			}
		}
		for _, s := range private {
			if err := strict.Check(make6(v4(s))); !errors.Is(err, ErrBlocked) {
				t.Errorf("strict: %s carrying %s is not blocked: %v", kind, s, err)
			}
			if err := allow.Check(make6(v4(s))); err != nil {
				t.Errorf("allow-private: %s carrying %s is blocked: %v", kind, s, err)
			}
		}
		for _, s := range public {
			for name, pol := range map[string]Policy{"strict": strict, "allow-private": allow} {
				if err := pol.Check(make6(v4(s))); err != nil {
					t.Errorf("%s: %s carrying %s is blocked: %v", name, kind, s, err)
				}
			}
		}
	}
	// Literal spellings of the same.
	for _, s := range []string{
		"::ffff:127.0.0.1", "::ffff:7f00:1", "0:0:0:0:0:ffff:a9fe:a9fe", "64:ff9b::7f00:1", "64:ff9b::127.0.0.1",
		"64:ff9b:1:7f00:0:1::", "2002:7f00:1::", "2001:0:4136:e378:8000:63bf:80ff:fffe",
		"::127.0.0.1", "::7f00:1", "::a9fe:a9fe", "::ffff:0:7f00:1", "::ffff:0:127.0.0.1",
	} {
		if err := allow.Check(netip.MustParseAddr(s)); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s is not blocked: %v", s, err)
		}
	}
	// 64:ff9b::/96 is not itself special: a public IPv4 address through NAT64
	// is how an IPv6-only cluster reaches the IPv4 internet.
	if err := strict.Check(netip.MustParseAddr("64:ff9b::808:808")); err != nil {
		t.Errorf("NAT64 to a public IPv4 address: %v", err)
	}
}

// A zone identifier does not change the verdict, and the zero address and
// every address that is not a canonical IP address in the first place is
// refused.
func TestPolicyCheckZonesAndInvalid(t *testing.T) {
	strict := NewPolicy(ModeStrict)
	for _, s := range []string{"fe80::1%eth0", "fe80::1%1", "::1%lo", "2001:db8::1%eth0", "::ffff:127.0.0.1%eth0", "fd12::1%eth0", "64:ff9b::7f00:1%x"} {
		if err := strict.Check(netip.MustParseAddr(s)); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s is not blocked: %v", s, err)
		}
	}
	for _, s := range []string{"2606:4700::1111%eth0", "::ffff:8.8.8.8%eth0"} {
		if err := strict.Check(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s is blocked: %v", s, err)
		}
	}
	if err := (Policy{}).Check(netip.Addr{}); !errors.Is(err, ErrBlocked) {
		t.Errorf("the zero address: %v", err)
	}
}
