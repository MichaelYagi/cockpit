// Package middleware contains the HTTP middleware applied to API routes.
package middleware

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"golang.org/x/time/rate"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/auth"
	"github.com/MichaelYagi/cockpit/internal/netaddr"
	"github.com/MichaelYagi/cockpit/internal/ulid"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// MaxBodyBytes is the request body limit (64 KiB).
const MaxBodyBytes = 64 << 10

// RequestID assigns every request a fresh ID. Incoming X-Request-ID values
// are ignored so clients cannot inject content into logs.
func RequestID(now func() time.Time) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := "req_" + ulid.New(now())
			w.Header().Set(apiv1.RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(httpx.WithRequestID(r.Context(), id)))
		})
	}
}

// Recover turns panics into a 500 error envelope. The panic is logged with a
// stack trace; the client sees only a generic message.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(v)
					}
					logger.Error("handler panic",
						"request_id", httpx.RequestID(r.Context()),
						"panic", fmt.Sprint(v),
						"stack", string(debug.Stack()))
					httpx.WriteError(w, r, http.StatusInternalServerError, apiv1.CodeInternal, "An unexpected error occurred.")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog logs one line per request: request ID, listener, method, route
// template, status, bytes, latency and principal. It never logs headers,
// query strings or bodies.
func AccessLog(logger *slog.Logger, listener string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			// Handlers attach the principal to their own request copy, so
			// capture it through a shared holder.
			holder := &principalHolder{}
			next.ServeHTTP(ww, r.WithContext(withHolder(r.Context(), holder)))

			route := "unmatched"
			if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
				route = rc.RoutePattern()
			}
			attrs := []any{
				"request_id", httpx.RequestID(r.Context()),
				"listener", listener,
				"method", r.Method,
				"route", route,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
			}
			if holder.set {
				attrs = append(attrs, "principal", holder.p.LogID())
				if holder.p.Kind == auth.KindLocalAdmin && holder.p.PeerUID >= 0 {
					attrs = append(attrs, "peer_uid", holder.p.PeerUID, "peer_pid", holder.p.PeerPID)
				}
			}
			level := slog.LevelInfo
			if ww.Status() >= 500 {
				level = slog.LevelError
			}
			logger.Log(r.Context(), level, "api request", attrs...)
		})
	}
}

// HostCheck rejects requests whose Host header is not a loopback name or
// address. Applied to loopback TCP listeners to block DNS rebinding.
func HostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !netaddr.IsLoopbackHostHeader(r.Host) {
			httpx.WriteError(w, r, http.StatusBadRequest, apiv1.CodeInvalidHost, "The Host header must name a loopback address.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CORS lets the listed web origins call the API from a browser (D34).
// Only GET is offered, the only method the TCP listener serves. A
// preflight from any other origin gets 403 origin_not_allowed; other
// requests from it are served without CORS headers, so the browser keeps
// the response from the page. No credentials (cookies) are involved:
// pages send the API key in the Authorization header.
func CORS(origins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		allowed[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			preflight := r.Method == http.MethodOptions && origin != "" && r.Header.Get("Access-Control-Request-Method") != ""
			if origin == "" || !allowed[origin] {
				if preflight {
					httpx.WriteError(w, r, http.StatusForbidden, apiv1.CodeOriginNotAllowed,
						"Browser access from this origin is not allowed. Add it to api.cors.allowed_origins in the agent configuration.")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
			if preflight {
				h.Set("Access-Control-Allow-Methods", "GET")
				h.Set("Access-Control-Allow-Headers", "Authorization, Accept, Last-Event-ID")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit rejects request bodies larger than limit.
func BodyLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, apiv1.CodeRequestTooLarge, "The request body is too large.")
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WriteDeadline bounds how long a response may take to write. It is set per
// request rather than server-wide so future streaming routes can opt out.
func WriteDeadline(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Not every ResponseWriter supports deadlines (e.g. in tests).
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
			next.ServeHTTP(w, r)
		})
	}
}

// LocalAdmin marks every request on the Unix socket as the local
// administrator principal (D3). Access control is the socket's filesystem
// permissions; peer credentials are recorded for audit.
func LocalAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, pid := -1, -1
		if p, ok := httpx.PeerFrom(r.Context()); ok {
			uid, pid = p.UID, p.PID
		}
		next.ServeHTTP(w, r.WithContext(setPrincipal(r.Context(), auth.LocalAdmin(uid, pid))))
	})
}

// Authenticate requires a valid bearer API key (D4). Every failure reason
// produces the same response so clients cannot probe key state. With
// limits set, failed attempts are rate limited per client IP and
// successful requests per key. Errors from the verifier other than
// auth.ErrInvalidCredentials are internal failures (500), not 401.
func Authenticate(v auth.Verifier, limits *Limits, stats *Stats, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p, ok := auth.PrincipalFrom(r.Context()); ok && p.Kind == auth.KindLocalAdmin {
				next.ServeHTTP(w, r) // socket requests are already authenticated
				return
			}
			// Failed attempts are limited per client IP. The key is always
			// verified first, so a flood of bad keys from one local process
			// (all loopback clients share 127.0.0.1) can never lock out a
			// client presenting a valid key.
			fail := func(code, msg, challenge string) {
				if stats != nil {
					stats.authFailures.Add(1)
				}
				if limits != nil && !limits.authFail.get(clientIP(r)).Allow() {
					rateLimited(w, r, retryAfter(authFailRPS), stats)
					return
				}
				w.Header().Set("WWW-Authenticate", challenge)
				httpx.WriteError(w, r, http.StatusUnauthorized, code, msg)
			}

			header := r.Header.Get("Authorization")
			if header == "" {
				fail(apiv1.CodeAuthenticationRequired, "A valid API key is required.", `Bearer realm="cockpit"`)
				return
			}
			scheme, token, found := strings.Cut(header, " ")
			token = strings.TrimSpace(token)
			if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
				fail(apiv1.CodeInvalidCredentials, "The API key is invalid.", `Bearer realm="cockpit", error="invalid_request"`)
				return
			}
			p, err := v.Verify(r.Context(), token)
			if errors.Is(err, auth.ErrInvalidCredentials) {
				fail(apiv1.CodeInvalidCredentials, "The API key is invalid.", `Bearer realm="cockpit", error="invalid_token"`)
				return
			}
			if err != nil {
				logger.Error("api key verification failed", "request_id", httpx.RequestID(r.Context()), "error", err)
				httpx.WriteError(w, r, http.StatusInternalServerError, apiv1.CodeInternal, "An unexpected error occurred.")
				return
			}
			if limits != nil && !limits.perKey.get(p.KeyID).Allow() {
				rateLimited(w, r, retryAfter(rate.Limit(limits.cfg.PerKeyRPS)), stats)
				return
			}
			if stats != nil {
				stats.seen(p.KeyID)
			}
			next.ServeHTTP(w, r.WithContext(setPrincipal(r.Context(), p)))
		})
	}
}

// RequireScope rejects principals that lack scope s.
func RequireScope(s auth.Scope) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := auth.PrincipalFrom(r.Context())
			if !ok {
				httpx.WriteError(w, r, http.StatusUnauthorized, apiv1.CodeAuthenticationRequired, "A valid API key is required.")
				return
			}
			if !p.Has(s) {
				httpx.WriteError(w, r, http.StatusForbidden, apiv1.CodeInsufficientScope,
					fmt.Sprintf("This key does not have the %s scope.", s))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
