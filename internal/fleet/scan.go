package fleet

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MichaelYagi/cockpit/pkg/client"
)

// Scan limits (D44).
const (
	DefaultScanPort = 7878
	// MaxScanHosts caps one scan (a /20). Larger home networks pass
	// --subnet for the part they use.
	MaxScanHosts     = 4096
	scanConcurrency  = 64
	scanDialTimeout  = 600 * time.Millisecond
	scanProbeTimeout = 2 * time.Second
)

// ScanOptions configures a LAN scan. Zero values use the defaults.
type ScanOptions struct {
	// Subnets to scan. Empty: the /24 around each private IPv4 address of
	// this machine.
	Subnets []netip.Prefix
	Port    int
	// DialTimeout bounds the TCP connect to each address.
	DialTimeout time.Duration
	// Dial replaces the TCP dialer (tests).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// LookupAddr replaces reverse DNS (tests); nil uses the system resolver.
	LookupAddr func(ctx context.Context, addr string) ([]string, error)
}

// Found is a Cockpit agent answering on the network.
type Found struct {
	Addr      netip.Addr
	URL       string // http://a.b.c.d:port or https://…
	Transport string // TransportHTTPS, TransportPlainLAN, TransportLoopback
	// Fingerprint is the SHA-256 of the server certificate (https only).
	// It is shown to compare, never trusted on its own: fleet add still
	// asks before pinning it.
	Fingerprint string
	// Name is a suggested fleet name from reverse DNS, or one built from
	// the address.
	Name string
}

// LocalSubnets returns the /24 around each private IPv4 address of this
// machine (or the interface's own prefix when it is narrower).
// Loopback and down interfaces are skipped (WSL puts 10.255.255.254 on lo).
func LocalSubnets() []netip.Prefix {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var addrs []net.Addr
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		if a, err := i.Addrs(); err == nil {
			addrs = append(addrs, a...)
		}
	}
	return localSubnets(addrs)
}

func localSubnets(in []net.Addr) []netip.Prefix {
	seen := map[netip.Prefix]bool{}
	var out []netip.Prefix
	for _, a := range in {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if !ip.Is4() || !ip.IsPrivate() {
			continue
		}
		bits, _ := ipn.Mask.Size()
		p, err := ip.Prefix(max(bits, 24))
		if err != nil || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// CheckSubnets rejects subnets a scan must not cover: only private and
// loopback IPv4 networks, at most MaxScanHosts addresses in total. A
// scan never probes the internet.
func CheckSubnets(subnets []netip.Prefix) error {
	total := 0
	for _, p := range subnets {
		a := p.Addr()
		if !a.Is4() {
			return fmt.Errorf("%s: only IPv4 subnets can be scanned", p)
		}
		if !a.IsPrivate() && !a.IsLoopback() {
			return fmt.Errorf("%s is not a private network; a scan only covers your own LAN", p)
		}
		if p.Bits() < 20 {
			return fmt.Errorf("%s is too large; scan at most a /20 (%d addresses)", p, MaxScanHosts)
		}
		total += 1 << (32 - p.Bits())
	}
	if total > MaxScanHosts {
		return fmt.Errorf("%d addresses is too many; scan at most %d at once", total, MaxScanHosts)
	}
	return nil
}

// Scan probes every address in the subnets for a Cockpit agent and
// returns those found, sorted by address. It sends no API key: it opens a
// TCP connection and asks the public /api/v1/livez route, over plain HTTP
// and then HTTPS. Addresses are probed concurrently with short timeouts,
// so a /24 takes a few seconds.
func Scan(ctx context.Context, o ScanOptions) ([]Found, error) {
	if o.Port == 0 {
		o.Port = DefaultScanPort
	}
	if o.Port < 1 || o.Port > 65535 {
		return nil, fmt.Errorf("port %d is out of range", o.Port)
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = scanDialTimeout
	}
	if len(o.Subnets) == 0 {
		o.Subnets = LocalSubnets()
		if len(o.Subnets) == 0 {
			return nil, errors.New("this machine has no private IPv4 address to scan around; pass --subnet")
		}
	}
	if err := CheckSubnets(o.Subnets); err != nil {
		return nil, err
	}
	if o.Dial == nil {
		o.Dial = (&net.Dialer{}).DialContext
	}
	if o.LookupAddr == nil {
		o.LookupAddr = net.DefaultResolver.LookupAddr
	}

	addrs := make(chan netip.Addr)
	var (
		mu    sync.Mutex
		found []Found
		wg    sync.WaitGroup
	)
	for range scanConcurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range addrs {
				if f, ok := probe(ctx, o, a); ok {
					mu.Lock()
					found = append(found, f)
					mu.Unlock()
				}
			}
		}()
	}
feed:
	for _, p := range o.Subnets {
		for a := p.Masked().Addr(); p.Contains(a); a = a.Next() {
			if p.Bits() < 31 && (a == p.Masked().Addr() || !p.Contains(a.Next())) {
				continue // network and broadcast addresses
			}
			select {
			case addrs <- a:
			case <-ctx.Done():
				break feed
			}
		}
	}
	close(addrs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Addr.Less(found[j].Addr) })
	return found, nil
}

func probe(ctx context.Context, o ScanOptions, a netip.Addr) (Found, bool) {
	hostport := netip.AddrPortFrom(a, uint16(o.Port)).String()
	dctx, cancel := context.WithTimeout(ctx, o.DialTimeout)
	conn, err := o.Dial(dctx, "tcp", hostport)
	cancel()
	if err != nil {
		return Found{}, false
	}
	conn.Close()

	f := Found{Addr: a}
	for _, scheme := range []string{"http", "https"} {
		u := scheme + "://" + hostport
		fp, ok := isCockpit(ctx, o, u)
		if !ok {
			continue
		}
		f.URL, f.Fingerprint = u, fp
		f.Transport = (Machine{URL: u}).Transport()
		f.Name = suggestName(ctx, o, a)
		return f, true
	}
	return Found{}, false
}

var requestID = regexp.MustCompile(`^req_[0-9A-HJKMNP-TV-Z]{26}$`)

// isCockpit asks base's public /api/v1/livez and reports whether the reply
// is a Cockpit agent's: exactly {"status":"ok"} with a Cockpit request ID.
// Other services on the same port (Radarr also defaults to 7878) don't
// match. For https it returns the certificate fingerprint.
func isCockpit(ctx context.Context, o ScanOptions, base string) (string, bool) {
	tr := &http.Transport{
		Proxy:             nil,
		DialContext:       o.Dial,
		DisableKeepAlives: true,
		// Read only: no key or other secret is sent, and the
		// certificate is shown for comparison, not trusted.
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // see above
		TLSHandshakeTimeout: scanProbeTimeout,
	}
	defer tr.CloseIdleConnections()
	c := &http.Client{
		Transport:     tr,
		Timeout:       scanProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/livez", nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil || resp.StatusCode != http.StatusOK || !requestID.MatchString(resp.Header.Get("X-Request-ID")) {
		return "", false
	}
	var v map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	if dec.Decode(&v) != nil || len(v) != 1 || v["status"] != "ok" {
		return "", false
	}
	var fp string
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		fp = client.CertificateFingerprint(resp.TLS.PeerCertificates[0].Raw)
	}
	return fp, true
}

var nameJunk = regexp.MustCompile(`[^a-z0-9._-]+`)

// suggestName returns the first label of the address's reverse DNS name,
// fitted to the fleet name rules, or "host-a-b-c-d".
func suggestName(ctx context.Context, o ScanOptions, a netip.Addr) string {
	lctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if names, err := o.LookupAddr(lctx, a.String()); err == nil && len(names) > 0 {
		label := strings.ToLower(strings.SplitN(strings.TrimSuffix(names[0], "."), ".", 2)[0])
		label = strings.Trim(nameJunk.ReplaceAllString(label, "-"), "-._")
		if len(label) > 64 {
			label = label[:64]
		}
		if namePattern.MatchString(label) {
			return label
		}
	}
	b := a.As4()
	return "host-" + strconv.Itoa(int(b[0])) + "-" + strconv.Itoa(int(b[1])) + "-" + strconv.Itoa(int(b[2])) + "-" + strconv.Itoa(int(b[3]))
}
