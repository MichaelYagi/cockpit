package middleware

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// LimitConfig configures rate limits for TCP listeners.
type LimitConfig struct {
	PerKeyRPS   float64
	PerKeyBurst int
	GlobalRPS   float64
	GlobalBurst int
}

// Failed authentication attempts per client IP. Keys have 256 bits of
// entropy, so this limits noise and log volume rather than guessing.
const (
	authFailRPS   = 1
	authFailBurst = 10
	maxTracked    = 4096
)

// Limits holds the rate limiters shared by all TCP requests.
type Limits struct {
	cfg      LimitConfig
	global   *rate.Limiter
	perKey   *keyedLimiter
	authFail *keyedLimiter
}

// NewLimits returns limiters for cfg.
func NewLimits(cfg LimitConfig) *Limits {
	return &Limits{
		cfg:      cfg,
		global:   rate.NewLimiter(rate.Limit(cfg.GlobalRPS), cfg.GlobalBurst),
		perKey:   newKeyed(rate.Limit(cfg.PerKeyRPS), cfg.PerKeyBurst),
		authFail: newKeyed(authFailRPS, authFailBurst),
	}
}

// keyedLimiter keeps one limiter per key with bounded memory.
type keyedLimiter struct {
	mu    sync.Mutex
	r     rate.Limit
	burst int
	m     map[string]*keyedEntry
}

type keyedEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newKeyed(r rate.Limit, burst int) *keyedLimiter {
	return &keyedLimiter{r: r, burst: burst, m: map[string]*keyedEntry{}}
}

func (k *keyedLimiter) get(id string) *rate.Limiter {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	if e, ok := k.m[id]; ok {
		e.seen = now
		return e.lim
	}
	if len(k.m) >= maxTracked {
		k.evict(now)
	}
	e := &keyedEntry{lim: rate.NewLimiter(k.r, k.burst), seen: now}
	k.m[id] = e
	return e.lim
}

// evict drops entries idle long enough to have refilled, then the oldest
// if still full. Caller holds k.mu.
func (k *keyedLimiter) evict(now time.Time) {
	refill := time.Duration(float64(k.burst)/float64(k.r)*float64(time.Second)) + time.Second
	var oldestID string
	var oldest time.Time
	for id, e := range k.m {
		if now.Sub(e.seen) > refill {
			delete(k.m, id)
			continue
		}
		if oldestID == "" || e.seen.Before(oldest) {
			oldestID, oldest = id, e.seen
		}
	}
	if len(k.m) >= maxTracked && oldestID != "" {
		delete(k.m, oldestID)
	}
}

func retryAfter(r rate.Limit) string {
	if r <= 0 {
		return "60"
	}
	return strconv.Itoa(int(math.Max(1, math.Ceil(1/float64(r)))))
}

func rateLimited(w http.ResponseWriter, r *http.Request, after string, stats *Stats) {
	if stats != nil {
		stats.rateLimited.Add(1)
	}
	w.Header().Set("Retry-After", after)
	httpx.WriteError(w, r, http.StatusTooManyRequests, apiv1.CodeRateLimited, "Too many requests. Retry after "+after+" seconds.")
}

// GlobalRateLimit bounds the total TCP request rate.
func GlobalRateLimit(l *Limits, stats *Stats) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.global.Allow() {
				rateLimited(w, r, retryAfter(rate.Limit(l.cfg.GlobalRPS)), stats)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP is the TCP peer address. Forwarding headers are not trusted.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Stats counts requests for the API Access screen. Safe for concurrent use.
type Stats struct {
	total, s2xx, s4xx, s5xx, authFailures, rateLimited atomic.Uint64

	mu      sync.Mutex
	clients map[string]time.Time // key ID -> last request
	now     func() time.Time
}

// ActiveWindow is how recently a key must have been used to count as an
// active client.
const ActiveWindow = 5 * time.Minute

// NewStats returns empty counters.
func NewStats(now func() time.Time) *Stats {
	if now == nil {
		now = time.Now
	}
	return &Stats{clients: map[string]time.Time{}, now: now}
}

// Count records the final status of every request.
func (s *Stats) Count(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.total.Add(1)
		switch {
		case rec.status >= 500:
			s.s5xx.Add(1)
		case rec.status >= 400:
			s.s4xx.Add(1)
		case rec.status >= 200 && rec.status < 300:
			s.s2xx.Add(1)
		}
	})
}

func (s *Stats) seen(keyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.clients) >= maxTracked {
		for id, t := range s.clients {
			if now.Sub(t) > ActiveWindow {
				delete(s.clients, id)
			}
		}
	}
	if len(s.clients) < maxTracked {
		s.clients[keyID] = now
	}
}

// Snapshot returns the counters and the number of active clients.
func (s *Stats) Snapshot() (apiv1.RequestStats, int) {
	st := apiv1.RequestStats{
		Total: s.total.Load(), Status2xx: s.s2xx.Load(), Status4xx: s.s4xx.Load(), Status5xx: s.s5xx.Load(),
		AuthFailures: s.authFailures.Load(), RateLimited: s.rateLimited.Load(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now, active := s.now(), 0
	for _, t := range s.clients {
		if now.Sub(t) <= ActiveWindow {
			active++
		}
	}
	return st, active
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status, r.wroteHeader = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
