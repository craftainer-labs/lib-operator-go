package naming

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strconv"
	"strings"
)

// HashLen is the number of base32 characters of the appended hash.
type HashLen int

const (
	// HashShort is 10 characters (50 bits), for backend objects that can hold the
	// ownership marker craftainer.dev/claim.
	HashShort HashLen = 10
	// HashLong is 16 characters (80 bits), for backend objects that cannot (Valkey
	// ACL users, RustFS users and service accounts, DCR client names).
	HashLong HashLen = 16
)

// minVisible is how many characters of ns+sep+name always survive the cut.
const minVisible = 5

// trimmed are the characters stripped from the end of the cut readable part,
// so no empty segment or doubled separator results.
const trimmed = "-._/:"

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Tenant returns the external name for the claim name in namespace ns:
// the readable ns+sep+name, cut so that the result fits maxLen, then sep and
// the hash. The hash is the first hl characters of the lower-case base32 of
// SHA-256(len(ns) ":" ns ":" name), so it never depends on sep. The result is
// never longer than maxLen and always ends in the hash.
//
// Tenant panics on a programmer error: hl is neither HashShort nor HashLong,
// sep is not exactly one byte, ns or name is empty, or maxLen is below
// hl+len(sep)+5. ns and name are Kubernetes-validated and are not re-checked.
func Tenant(ns, name string, maxLen int, sep string, hl HashLen) string {
	switch {
	case hl != HashShort && hl != HashLong:
		panic(fmt.Sprintf("naming.Tenant: hash length %d is neither HashShort nor HashLong", hl))
	case len(sep) != 1:
		panic(fmt.Sprintf("naming.Tenant: separator %q must be exactly one byte", sep))
	case ns == "" || name == "":
		panic("naming.Tenant: namespace and name must not be empty")
	case maxLen < int(hl)+len(sep)+minVisible:
		panic(fmt.Sprintf("naming.Tenant: maxLen %d is below the minimum %d", maxLen, int(hl)+len(sep)+minVisible))
	}
	sum := sha256.Sum256([]byte(strconv.Itoa(len(ns)) + ":" + ns + ":" + name))
	hash := strings.ToLower(encoding.EncodeToString(sum[:]))[:hl]
	readable := ns + sep + name
	if keep := maxLen - len(sep) - int(hl); len(readable) > keep {
		readable = readable[:keep]
	}
	return strings.TrimRight(readable, trimmed) + sep + hash
}
