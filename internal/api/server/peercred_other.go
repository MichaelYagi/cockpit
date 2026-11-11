//go:build !linux && !windows

package server

import (
	"net"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
)

// peerCredentials is not implemented on this platform yet.
func peerCredentials(net.Conn) (httpx.Peer, bool) { return httpx.Peer{}, false }
