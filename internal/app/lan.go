package app

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/config"
	"github.com/MichaelYagi/cockpit/internal/netaddr"
	"github.com/MichaelYagi/cockpit/internal/tlsutil"
	"github.com/MichaelYagi/cockpit/pkg/client"
)

// lanInfo is what the person who turned on api.lan needs to connect from
// another machine (D37).
type lanInfo struct {
	hostname    string
	addrs       []netip.Addr
	fingerprint string // colon-separated upper-case hex, as browsers show it
	plain       bool   // api.lan.http: no TLS (D38)
}

// prepareLAN makes sure the agent's own certificate covers this machine's
// current names and addresses (when api.tls names no files), and collects
// the addresses to show.
func prepareLAN(cfg config.Config, paths config.Paths, log *slog.Logger, now time.Time) (*lanInfo, error) {
	info := &lanInfo{addrs: netaddr.LANAddresses(), plain: cfg.API.LAN.HTTP}
	if h, err := os.Hostname(); err == nil {
		info.hostname = h
	}
	if len(info.addrs) == 0 {
		log.Warn("api.lan is on but no network address was found; check that this machine is connected")
	}
	if info.plain {
		return info, nil
	}
	certPath := cfg.API.TLS.CertFile
	if paths.TLSAutoDir != "" {
		cert, issued, err := tlsutil.Ensure(paths.TLSAutoDir, lanHosts(info.hostname, info.addrs), now)
		if err != nil {
			return nil, fmt.Errorf("LAN certificate in %s: %w", paths.TLSAutoDir, err)
		}
		if issued {
			log.Info("issued a TLS certificate for LAN access", "dir", paths.TLSAutoDir, "for", strings.Join(certNames(cert.DNSNames, cert.IPAddresses), ", "))
		}
	}
	cert, err := tlsutil.ReadCertificate(certPath)
	if err != nil {
		return nil, fmt.Errorf("read TLS certificate: %w", err)
	}
	info.fingerprint = colonHex(client.CertificateFingerprint(cert.Raw))
	return info, nil
}

// lanHosts are the names the agent's own certificate is issued for.
func lanHosts(hostname string, addrs []netip.Addr) []string {
	var hosts []string
	if hostname != "" {
		hosts = append(hosts, hostname)
	}
	hosts = append(hosts, "localhost", "127.0.0.1", "::1")
	for _, a := range addrs {
		hosts = append(hosts, a.String())
	}
	return hosts
}

func certNames(dns []string, ips []net.IP) []string {
	out := append([]string{}, dns...)
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out
}

// colonHex turns "ab12…" into "AB:12:…".
func colonHex(h string) string {
	h = strings.ToUpper(h)
	var b strings.Builder
	for i := 0; i+1 < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// write prints how to connect from another machine. It never prints a key.
func (l *lanInfo) write(w io.Writer, listen string, mode config.Mode) {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		port = "7878"
	}
	scheme := "https://"
	if l.plain {
		scheme = "http://"
	}
	var urls []string
	for _, a := range l.addrs {
		urls = append(urls, scheme+net.JoinHostPort(a.String(), port)+"/")
	}
	if l.hostname != "" {
		urls = append(urls, scheme+net.JoinHostPort(l.hostname, port)+"/   (if this name works on your network)")
	}
	first := scheme + "HOST:" + port
	if len(l.addrs) > 0 {
		first = scheme + net.JoinHostPort(l.addrs[0].String(), port)
	}
	keys := "cockpit keys create --name laptop --scope all"
	if mode == config.ModeSystem {
		keys = "cockpit keys create --mode system --name laptop --scope all"
	}
	if l.plain {
		fmt.Fprintf(w, `
LAN access is on over plain HTTP. API keys and data travel unencrypted:
anyone on this network can read them. Only private-network addresses may
connect. From another computer on this network, open:
  %s

Sign in with an API key. Create one on this machine (shown once):
  %s

Or call the API:
  curl -H "Authorization: Bearer $COCKPIT_API_KEY" %s/api/v1/health

Or use the TUI from another computer:
  export COCKPIT_API_KEY=<the key>
  cockpit tui --url %s

`, strings.Join(urls, "\n  "), keys, first, first)
		return
	}
	fmt.Fprintf(w, `
LAN access is on. From another computer on this network, open:
  %s

The browser warns about the certificate the first time, because this
agent made it. Continue if the certificate's SHA-256 fingerprint is:
  %s

Sign in with an API key. Create one on this machine (shown once):
  %s

Or use the TUI from another computer:
  export COCKPIT_API_KEY=<the key>
  cockpit tui --url %s --tls-fingerprint %s

`, strings.Join(urls, "\n  "), l.fingerprint, keys, first, l.fingerprint)
}
