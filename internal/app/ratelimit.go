package app

import (
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// limiter throttles the endpoints worth guessing against — login and
// registration — one bucket per client address.
//
// Nothing else is limited. Pointing this at the polling route would lock out
// every reader with a tab open, because that is the one route they all hit on a
// timer (patterns/htmx-live-updates.md).
type limiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	lastSeen time.Time
}

type bucket struct {
	*rate.Limiter
	seen time.Time
}

func newLimiter() *limiter {
	return &limiter{buckets: make(map[string]*bucket)}
}

// allow reports whether this address may make another attempt: five to begin
// with, then one every three seconds.
func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{Limiter: rate.NewLimiter(rate.Every(3*time.Second), 5)}
		l.buckets[ip] = b
	}
	b.seen = now

	// Evict quietly, on the same lock, instead of running a goroutine for a map
	// that only grows while somebody is failing to log in.
	if now.Sub(l.lastSeen) > time.Hour {
		for addr, old := range l.buckets {
			if now.Sub(old.seen) > time.Hour {
				delete(l.buckets, addr)
			}
		}
		l.lastSeen = now
	}
	return b.Allow()
}

// limiterKey is the bucket a client address falls into: the address itself, or
// for IPv6 its /64. One subscriber holds a whole /64 and can take a fresh
// address out of it for every guess, so keyed on the full address the limiter
// would never see that client twice — and its map would grow by one entry per
// guess (patterns/go-auth-sessions.md).
//
// Unmap comes first. An IPv4 client that arrives written as IPv6,
// ::ffff:192.0.2.10, is that IPv4 client: cut to a /64 as it stands, every such
// address would land in the one bucket ::/64, and left as text it would have a
// bucket beside its own for each way of spelling it. Anything that is not an
// address keys on itself.
func limiterKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	if addr = addr.Unmap(); addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr.WithZone(""), 64).Masked().String()
}

// clientIP trusts X-Forwarded-For because nothing but the proxy can reach this
// app, and the proxy overwrote whatever the client sent
// (operations/web-application.md). The header holds one address, not a chain —
// nothing to split, no last-hop rule to get wrong.
//
// The RemoteAddr fallback is not decoration: with no proxy the header is empty,
// and a limiter keyed on "" throttles every visitor as if they were one.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr) // no proxy in front: dev
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
