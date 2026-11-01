package naming_test

import (
	"crypto/rand"
	"math/big"
	"strings"
	"testing"

	"github.com/craftainer-labs/lib-operator-go/pkg/naming"
)

// The ADR's worked example is the test vector.
func TestTenantVectors(t *testing.T) {
	tests := []struct {
		ns, name string
		maxLen   int
		sep      string
		hl       naming.HashLen
		want     string
	}{
		{"team-a", "orders", 63, "_", naming.HashShort, "team-a_orders_gdeqxvirep"},
		{"team-a", "orders", 63, "-", naming.HashShort, "team-a-orders-gdeqxvirep"},
		{"team-a", "orders", 128, ":", naming.HashLong, "team-a:orders:gdeqxvirepf7uebv"},
		// The smallest maxLen for HashShort keeps 5 characters, "team-", and the
		// trailing "-" is stripped before the separator is appended.
		{"team-a", "orders", 16, "-", naming.HashShort, "team-gdeqxvirep"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := naming.Tenant(tt.ns, tt.name, tt.maxLen, tt.sep, tt.hl); got != tt.want {
				t.Errorf("Tenant = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTenantShortHashIsPrefixOfLong(t *testing.T) {
	short := naming.Tenant("team-a", "orders", 128, ":", naming.HashShort)
	long := naming.Tenant("team-a", "orders", 128, ":", naming.HashLong)
	if !strings.HasPrefix(long, short) {
		t.Errorf("%q is not a prefix of %q", short, long)
	}
}

func TestTenantCutStripsTrailingSeparators(t *testing.T) {
	// ns+sep+name = "aaaa-bbbb.-cccc"; maxLen 22 keeps 11 characters, "aaaa-bbbb.-",
	// and the cut strips ".-" before the separator and hash.
	got := naming.Tenant("aaaa", "bbbb.-cccc", 22, "-", naming.HashShort)
	if !strings.HasPrefix(got, "aaaa-bbbb-") || len(got) != len("aaaa-bbbb-")+10 {
		t.Errorf("Tenant = %q, want aaaa-bbbb-<hash>", got)
	}
	// Each strip character is removed when the cut lands right after it:
	// "team-name<c>x" keeps "team-name<c>".
	for _, c := range []string{"-", ".", "_", "/", ":"} {
		got := naming.Tenant("team", "name"+c+"x", 10+1+10, "-", naming.HashShort)
		if !strings.HasPrefix(got, "team-name-") || len(got) != len("team-name-")+10 {
			t.Errorf("strip %q: Tenant = %q, want team-name-<hash>", c, got)
		}
	}
}

func TestTenantPanics(t *testing.T) {
	tests := []struct {
		name string
		call func()
	}{
		{"hash length", func() { naming.Tenant("a", "b", 63, "-", 7) }},
		{"empty separator", func() { naming.Tenant("a", "b", 63, "", naming.HashShort) }},
		{"long separator", func() { naming.Tenant("a", "b", 63, "--", naming.HashShort) }},
		{"empty namespace", func() { naming.Tenant("", "b", 63, "-", naming.HashShort) }},
		{"empty name", func() { naming.Tenant("a", "", 63, "-", naming.HashShort) }},
		{"maxLen below the short minimum", func() { naming.Tenant("a", "b", 15, "-", naming.HashShort) }},
		{"maxLen below the long minimum", func() { naming.Tenant("a", "b", 21, "-", naming.HashLong) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("Tenant did not panic")
				}
			}()
			tt.call()
		})
	}
}

// The minimum maxLen is accepted and honoured.
func TestTenantMinimumMaxLen(t *testing.T) {
	if got := naming.Tenant("team", "x", 16, "-", naming.HashShort); len(got) > 16 {
		t.Errorf("len(%q) = %d, want at most 16", got, len(got))
	}
	if got := naming.Tenant("team", "x", 22, "-", naming.HashLong); len(got) > 22 {
		t.Errorf("len(%q) = %d, want at most 22", got, len(got))
	}
}

// source draws test inputs from crypto/rand; a failing case prints its inputs.
type source struct{}

func (source) IntN(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err)
	}
	return int(v.Int64())
}

func randomLabel(r source, n int, dots bool) string {
	alphanumeric := "abcdefghijklmnopqrstuvwxyz0123456789"
	alphabet := alphanumeric + "-"
	if dots {
		alphabet += ".."
	}
	b := make([]byte, 1+r.IntN(n))
	for i := range b {
		b[i] = alphabet[r.IntN(len(alphabet))]
	}
	b[0] = alphanumeric[r.IntN(len(alphanumeric))]
	return string(b)
}

// Property test: the result fits maxLen, keeps at least 5 visible characters,
// ends in sep and the hash with no stripped character before the separator,
// and distinct (ns, name) pairs never produce one result.
func TestTenantProperties(t *testing.T) {
	r := source{}
	seen := map[string]string{}
	for range 20000 {
		ns, name := randomLabel(r, 20, false), randomLabel(r, 60, true)
		sep := []string{"-", "_", ":", "/"}[r.IntN(4)]
		hl := []naming.HashLen{naming.HashShort, naming.HashLong}[r.IntN(2)]
		maxLen := int(hl) + 1 + 5 + r.IntN(100)

		got := naming.Tenant(ns, name, maxLen, sep, hl)
		full := naming.Tenant(ns, name, 128, sep, hl) // never cut: ns+sep+name < 112
		hash := full[len(full)-int(hl):]
		if len(got) > maxLen {
			t.Fatalf("Tenant(%q,%q,%d) = %q is longer than maxLen", ns, name, maxLen, got)
		}
		if !strings.HasSuffix(got, sep+hash) {
			t.Fatalf("Tenant(%q,%q,%d) = %q does not end in %q", ns, name, maxLen, got, sep+hash)
		}
		cut := strings.TrimSuffix(got, sep+hash)
		if cut == "" || strings.ContainsAny(cut[len(cut)-1:], "-._/:") {
			t.Fatalf("Tenant(%q,%q,%d) = %q has an empty or unstripped visible part", ns, name, maxLen, got)
		}
		id := got + "|" + sep
		if prev, ok := seen[id]; ok && prev != ns+"/"+name {
			t.Fatalf("collision: %q and %q both give %q", prev, ns+"/"+name, got)
		}
		seen[id] = ns + "/" + name
	}
}

// ("team-a", "db") and ("team", "a-db") read the same on a "-" backend; the hash
// keeps them apart.
func TestTenantAmbiguousReadablePart(t *testing.T) {
	a := naming.Tenant("team-a", "db", 63, "-", naming.HashShort)
	b := naming.Tenant("team", "a-db", 63, "-", naming.HashShort)
	if a == b {
		t.Errorf("both give %q", a)
	}
	if !strings.HasPrefix(a, "team-a-db-") || !strings.HasPrefix(b, "team-a-db-") {
		t.Errorf("readable parts differ: %q, %q", a, b)
	}
}
