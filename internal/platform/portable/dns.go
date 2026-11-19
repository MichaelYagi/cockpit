package portable

import (
	"bufio"
	"bytes"
	"net/netip"
	"strings"

	"github.com/MichaelYagi/cockpit/internal/core"
)

// ParseResolvConf reads nameservers (at most 16) and the search list
// from resolv.conf.
func ParseResolvConf(b []byte) core.DNSConfig {
	var cfg core.DNSConfig
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || strings.HasPrefix(f[0], "#") || strings.HasPrefix(f[0], ";") {
			continue
		}
		switch f[0] {
		case "nameserver":
			if _, err := netip.ParseAddr(f[1]); err == nil && len(cfg.Nameservers) < 16 {
				cfg.Nameservers = append(cfg.Nameservers, f[1])
			}
		case "search", "domain":
			cfg.Search = append(cfg.Search[:0], f[1:]...)
		}
	}
	return cfg
}
