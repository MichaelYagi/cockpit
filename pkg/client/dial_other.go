//go:build !windows

package client

import (
	"context"
	"net"
)

func dialAdmin(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}
