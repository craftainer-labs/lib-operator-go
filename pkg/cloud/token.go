package cloud

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DefaultRefreshMargin is how long before its expiry a CachedSource stops
// handing out a token.
const DefaultRefreshMargin = time.Minute

// TokenSource returns a token for an IAM login of a data plane and the time it
// expires; the zero time means the source does not know. It does not cache.
type TokenSource interface {
	Token(ctx context.Context) (token string, expiry time.Time, err error)
}

// TokenFunc is a TokenSource.
type TokenFunc func(ctx context.Context) (string, time.Time, error)

// Token implements TokenSource.
func (f TokenFunc) Token(ctx context.Context) (string, time.Time, error) { return f(ctx) }

// CachedSource reuses a token until a margin before it expires, so a token is
// fetched just before a connect and never handed out when it is about to run
// out. It is safe for concurrent use, and one fetch serves every waiting
// caller. A token whose expiry is unknown (the zero time) or already inside the
// margin is returned but not kept. A failed fetch is returned as it is; the
// previous token is dropped and not served stale.
type CachedSource struct {
	src    TokenSource
	margin time.Duration
	now    func() time.Time

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// NewCachedSource wraps src. A margin of zero or less means
// DefaultRefreshMargin.
func NewCachedSource(src TokenSource, margin time.Duration) *CachedSource {
	if margin <= 0 {
		margin = DefaultRefreshMargin
	}
	return &CachedSource{src: src, margin: margin, now: time.Now}
}

// Token implements TokenSource. The lock is held across the fetch, so callers
// that wait do not give up when their own context ends.
func (c *CachedSource) Token(ctx context.Context) (string, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Add(c.margin).Before(c.expiry) {
		return c.token, c.expiry, nil
	}
	c.token, c.expiry = "", time.Time{}
	tok, exp, err := c.src.Token(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	if tok == "" {
		return "", time.Time{}, fmt.Errorf("%w: the token source returned no token", ErrCredentialsUnavailable)
	}
	if !exp.IsZero() && c.now().Add(c.margin).Before(exp) {
		c.token, c.expiry = tok, exp
	}
	return tok, exp, nil
}

// Invalidate drops the cached token, for example after the data plane
// rejected it.
func (c *CachedSource) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token, c.expiry = "", time.Time{}
}
