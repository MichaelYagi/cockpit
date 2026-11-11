// Package server owns the API listeners: the local admin Unix socket (D3)
// and the key-authenticated TCP listener (D4), optionally with TLS.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/netaddr"
)

// Config describes which listeners to open.
type Config struct {
	Socket SocketConfig
	TCP    TCPConfig
}

// SocketConfig configures the Unix socket. The parent directory must
// already exist with restrictive permissions (created by the app).
type SocketConfig struct {
	Enabled bool
	Path    string
	Mode    fs.FileMode // 0600 (user mode) or 0660 (system mode)
}

// TCPConfig configures the TCP listener.
type TCPConfig struct {
	Enabled  bool
	Address  string
	CertFile string // TLS is enabled when both files are set
	KeyFile  string
}

// Endpoints are the addresses actually listening.
type Endpoints struct {
	SocketPath string
	TCPAddress string // resolved, e.g. 127.0.0.1:7878 or the port chosen for :0
	TCPTLS     bool
}

// Server serves API handlers on the configured listeners.
type Server struct {
	cfg    Config
	socket http.Handler
	tcp    http.Handler
	logger *slog.Logger

	servers  []*http.Server
	sockPath string
	errs     chan error
	wg       sync.WaitGroup

	// baseCtx is the parent of every request context. Shutdown cancels it
	// first so long-lived requests (event streams) end promptly instead of
	// holding up a graceful shutdown.
	baseCtx    context.Context
	cancelBase context.CancelFunc
}

// New creates a server. socketHandler and tcpHandler serve the respective
// listeners.
func New(cfg Config, socketHandler, tcpHandler http.Handler, logger *slog.Logger) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{cfg: cfg, socket: socketHandler, tcp: tcpHandler, logger: logger, errs: make(chan error, 2), baseCtx: ctx, cancelBase: cancel}
}

// Start opens every listener and begins serving. If any listener fails,
// those already opened are closed and the error is returned.
func (s *Server) Start() (Endpoints, error) {
	var ep Endpoints
	var listeners []net.Listener
	fail := func(err error) (Endpoints, error) {
		for _, l := range listeners {
			_ = l.Close()
		}
		if s.sockPath != "" {
			_ = os.Remove(s.sockPath)
			s.sockPath = ""
		}
		return Endpoints{}, err
	}

	if s.cfg.Socket.Enabled {
		l, err := listenUnix(s.cfg.Socket.Path, s.cfg.Socket.Mode)
		if err != nil {
			return fail(err)
		}
		listeners = append(listeners, l)
		s.sockPath = s.cfg.Socket.Path
		ep.SocketPath = s.cfg.Socket.Path
		srv := s.newHTTPServer(s.socket)
		srv.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
			if p, ok := peerCredentials(c); ok {
				return httpx.WithPeer(ctx, p)
			}
			return ctx
		}
		s.serve(srv, l, "socket")
	}

	if s.cfg.TCP.Enabled {
		l, tlsOn, err := s.listenTCP()
		if err != nil {
			return fail(err)
		}
		listeners = append(listeners, l)
		ep.TCPAddress, ep.TCPTLS = l.Addr().String(), tlsOn
		s.serve(s.newHTTPServer(s.tcp), l, "tcp")
	}
	return ep, nil
}

func (s *Server) listenTCP() (net.Listener, bool, error) {
	host, _, err := net.SplitHostPort(s.cfg.TCP.Address)
	if err != nil {
		return nil, false, fmt.Errorf("tcp address %q: %w", s.cfg.TCP.Address, err)
	}
	loopback, _ := netaddr.IsLoopbackHost(host)
	tlsOn := s.cfg.TCP.CertFile != "" && s.cfg.TCP.KeyFile != ""
	// Defense in depth: configuration validation also enforces this.
	if !loopback && !tlsOn {
		return nil, false, fmt.Errorf("refusing to listen on non-loopback address %q without TLS", s.cfg.TCP.Address)
	}

	var tlsCfg *tls.Config
	if tlsOn {
		if err := checkKeyFilePermissions(s.cfg.TCP.KeyFile); err != nil {
			return nil, false, err
		}
		cert, err := tls.LoadX509KeyPair(s.cfg.TCP.CertFile, s.cfg.TCP.KeyFile)
		if err != nil {
			return nil, false, fmt.Errorf("load TLS certificate: %w", err)
		}
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}

	l, err := net.Listen("tcp", s.cfg.TCP.Address)
	if err != nil {
		return nil, false, fmt.Errorf("listen on %s: %w", s.cfg.TCP.Address, err)
	}
	if tlsCfg != nil {
		l = tls.NewListener(l, tlsCfg)
	}
	if !loopback {
		s.logger.Warn("API is listening on a non-loopback address; every request requires TLS and an API key",
			"address", l.Addr().String())
	}
	return l, tlsOn, nil
}

func (s *Server) newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// No server-wide WriteTimeout: it would cut off future SSE streams.
		// Write deadlines are set per route by middleware.
		ErrorLog:    slog.NewLogLogger(s.logger.Handler(), slog.LevelWarn),
		BaseContext: func(net.Listener) context.Context { return s.baseCtx },
	}
}

func (s *Server) serve(srv *http.Server, l net.Listener, name string) {
	s.servers = append(s.servers, srv)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.errs <- fmt.Errorf("%s listener: %w", name, err)
		}
	}()
}

// Errors reports listeners that stopped unexpectedly.
func (s *Server) Errors() <-chan error { return s.errs }

// Shutdown stops accepting connections, waits for in-flight requests until
// ctx expires, then closes remaining connections and removes the socket.
func (s *Server) Shutdown(ctx context.Context) error {
	s.cancelBase()
	var errs []error
	for _, srv := range s.servers {
		if err := srv.Shutdown(ctx); err != nil {
			errs = append(errs, err)
			_ = srv.Close()
		}
	}
	s.wg.Wait()
	if s.sockPath != "" {
		if err := os.Remove(s.sockPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
