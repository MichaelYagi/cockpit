package portable

import (
	"strings"
	"testing"
)

func TestParseResolvConf(t *testing.T) {
	c := ParseResolvConf([]byte("# macOS\nnameserver 192.168.1.1\nnameserver fe80::1%en0\nnameserver bogus\n; x\nsearch lan example.com\n"))
	if strings.Join(c.Nameservers, ",") != "192.168.1.1,fe80::1%en0" || strings.Join(c.Search, ",") != "lan,example.com" {
		t.Errorf("%+v", c)
	}
}
