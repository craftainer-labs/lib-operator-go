package cloud

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/craftainer-labs/lib-operator-go/pkg/egress"
)

// openEgress is a cluster server's environment in allow-private mode whose
// Policy blocks nothing, so the httptest servers on loopback stand for the
// private-address emulators of CI. The refusal tests use NewEgress, whose
// Policy blocks loopback in both modes.
func openEgress() Egress { return Egress{Mode: egress.ModeAllowPrivate} }

// writeFile writes a file under the test's temp directory.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// hits counts the requests a fake server answered.
type hits struct {
	n     atomic.Int32
	mu    sync.Mutex
	form  map[string][]string
	authz string
}

func (h *hits) record(r *http.Request) {
	h.n.Add(1)
	_ = r.ParseForm()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.form = r.Form
	h.authz = r.Header.Get("Authorization")
}

func (h *hits) count() int { return int(h.n.Load()) }

func (h *hits) field(k string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.form[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

var (
	keyOnce sync.Once
	keyPEM  string
)

// serviceAccountJSON is a service_account key with a freshly generated private
// key (once per test binary) and the given token_uri.
func serviceAccountJSON(t *testing.T, tokenURI string) []byte {
	t.Helper()
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	})
	return []byte(`{"type":"service_account","project_id":"proj-12345","private_key_id":"kid",` +
		`"private_key":` + jsonString(keyPEM) + `,"client_email":"sa@proj-12345.iam.gserviceaccount.com",` +
		`"client_id":"1","token_uri":` + jsonString(tokenURI) + `}`)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// noSecret fails the test if any of the values is in err's text.
func noSecret(t *testing.T, err error, values ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error")
	}
	for _, v := range values {
		if v != "" && strings.Contains(err.Error(), v) {
			t.Errorf("error %q contains the secret %q", err, v)
		}
	}
}

// newServer starts a fake that counts hits and answers with h.
func newServer(t *testing.T, h func(http.ResponseWriter, *http.Request)) (*httptest.Server, *hits) {
	t.Helper()
	return newServerRaw(t, h, false)
}

// newServerRaw is newServer, optionally over TLS.
func newServerRaw(t *testing.T, h func(http.ResponseWriter, *http.Request), useTLS bool) (*httptest.Server, *hits) {
	t.Helper()
	c := &hits{}
	hf := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		h(w, r)
	})
	var s *httptest.Server
	if useTLS {
		s = httptest.NewTLSServer(hf)
	} else {
		s = httptest.NewServer(hf)
	}
	t.Cleanup(s.Close)
	return s, c
}

// doErr sends req and returns the error, closing any response.
func doErr(c *http.Client, req *http.Request) error {
	resp, err := c.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
}

// getErr GETs the URL and returns the error, closing any response.
func getErr(c *http.Client, u string) error {
	resp, err := c.Get(u)
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
}

func (h *hits) authorization() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.authz
}
