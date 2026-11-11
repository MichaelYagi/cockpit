// Package report post-processes diagnostic reports (D21): anonymization
// and Markdown rendering. It works on the public wire types only.
package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// AnonymousMachineID replaces the machine ID in anonymized reports, so two
// reports cannot be linked to the same machine by it.
const AnonymousMachineID = "00000000-0000-4000-8000-000000000000"

// Fields whose values are network addresses (possibly with a prefix
// length or port).
var addressKeys = map[string]bool{"addresses": true, "destination": true, "gateway": true, "nameservers": true, "address": true}

var (
	macPattern  = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(?:[:-][0-9a-f]{2}){5}\b`)
	homePattern = regexp.MustCompile(`/home/([^/\s"'` + "`" + `]+)`)
)

// Anonymize returns a copy of r with identifying values replaced by stable
// placeholders: the host name and DNS search domains, non-loopback IP
// addresses (by documentation addresses), MAC-like strings, user names
// taken from /home paths, and the machine ID. The same value always maps
// to the same placeholder within one report.
//
// Identifiers are collected from the fields that hold them, then replaced
// wherever they appear (including free text such as event messages), so
// version numbers and similar strings are not mistaken for addresses.
func Anonymize(r *apiv1.Report) (*apiv1.Report, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	a := newAnonymizer()
	a.collect("", tree)
	tree = a.rewrite("", tree)
	raw, err = json.Marshal(tree)
	if err != nil {
		return nil, err
	}
	out := new(apiv1.Report)
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, err
	}
	out.Anonymized = true
	return out, nil
}

type anonymizer struct {
	// words maps identifying strings (host names, domains, user names, IP
	// addresses) to placeholders, replaced at token boundaries.
	words map[string]string
	ips   map[netip.Addr]netip.Addr
	macs  map[string]string
	users map[string]string
	hosts int
	doms  int
	v4    int
	v6    int
}

func newAnonymizer() *anonymizer {
	return &anonymizer{words: map[string]string{}, ips: map[netip.Addr]netip.Addr{}, macs: map[string]string{}, users: map[string]string{}}
}

// collect records identifiers found in identifying fields.
func (a *anonymizer) collect(key string, v any) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			a.collect(k, t[k])
		}
	case []any:
		for _, c := range t {
			a.collect(key, c)
		}
	case string:
		switch {
		case key == "hostname":
			a.host(t)
		case key == "search":
			a.domain(t)
		case addressKeys[key]:
			if ip, ok := parseAddress(t); ok {
				a.ip(ip)
			}
		}
		for _, m := range homePattern.FindAllStringSubmatch(t, -1) {
			a.user(m[1])
		}
	}
}

func (a *anonymizer) host(h string) {
	h = asciiLower(strings.TrimSuffix(h, "."))
	if h == "" || h == "localhost" {
		return
	}
	if _, ok := a.words[h]; ok {
		return
	}
	a.hosts++
	p := fmt.Sprintf("host-%d", a.hosts)
	a.words[h] = p
	if short, _, ok := strings.Cut(h, "."); ok && len(short) >= 3 {
		if _, seen := a.words[short]; !seen {
			a.words[short] = p
		}
	}
}

func (a *anonymizer) domain(d string) {
	d = asciiLower(strings.TrimSuffix(d, "."))
	if d == "" {
		return
	}
	if _, ok := a.words[d]; !ok {
		a.doms++
		a.words[d] = fmt.Sprintf("domain-%d.example", a.doms)
	}
}

func (a *anonymizer) user(u string) {
	if _, ok := a.users[u]; ok {
		return
	}
	p := fmt.Sprintf("user-%d", len(a.users)+1)
	a.users[u] = p
	// Bare mentions of longer names are replaced too; very short names
	// would match ordinary words.
	if len(u) >= 3 {
		if _, ok := a.words[asciiLower(u)]; !ok {
			a.words[asciiLower(u)] = p
		}
	}
}

// ip maps a non-loopback, non-multicast address to a documentation
// address (RFC 5737, RFC 3849).
func (a *anonymizer) ip(ip netip.Addr) {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return
	}
	if _, ok := a.ips[ip]; ok {
		return
	}
	var p netip.Addr
	if ip.Is4() {
		nets := [][3]byte{{192, 0, 2}, {198, 51, 100}, {203, 0, 113}}
		n := a.v4 % (len(nets) * 254)
		a.v4++
		b := nets[n/254]
		p = netip.AddrFrom4([4]byte{b[0], b[1], b[2], byte(n%254 + 1)})
	} else {
		a.v6++
		var b [16]byte
		b[0], b[1], b[2], b[3] = 0x20, 0x01, 0x0d, 0xb8
		b[14], b[15] = byte(a.v6>>8), byte(a.v6)
		p = netip.AddrFrom16(b)
	}
	a.ips[ip] = p
	a.words[asciiLower(ip.String())] = p.String()
}

// parseAddress accepts an address, a prefix ("10.0.0.0/8") or an
// address with a port ("192.0.2.1:80", "[::1]:80").
func parseAddress(s string) (netip.Addr, bool) {
	if ip, err := netip.ParseAddr(s); err == nil {
		return ip, true
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Addr(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr(), true
	}
	return netip.Addr{}, false
}

// sortedKeys orders the walk, so placeholders are numbered the same way
// for the same input.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// rewrite replaces identifiers in every string value.
func (a *anonymizer) rewrite(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			t[k] = a.rewrite(k, t[k])
		}
		return t
	case []any:
		for i, c := range t {
			t[i] = a.rewrite(key, c)
		}
		return t
	case string:
		if key == "machine_id" {
			return AnonymousMachineID
		}
		return a.text(t)
	}
	return v
}

// text replaces identifiers inside one string.
func (a *anonymizer) text(s string) string {
	s = macPattern.ReplaceAllStringFunc(s, func(m string) string {
		k := strings.ToLower(strings.ReplaceAll(m, "-", ":"))
		p, ok := a.macs[k]
		if !ok {
			n := len(a.macs) + 1
			p = fmt.Sprintf("02:00:00:00:%02x:%02x", byte(n>>8), byte(n))
			a.macs[k] = p
		}
		return p
	})
	s = homePattern.ReplaceAllStringFunc(s, func(m string) string {
		return "/home/" + a.users[strings.TrimPrefix(m, "/home/")]
	})
	return a.replaceWords(s)
}

// replaceWords replaces every identifier in a.words in one pass, so a
// placeholder is never rewritten again. Matching is ASCII case-insensitive
// and only at token boundaries; the longest identifier wins.
func (a *anonymizer) replaceWords(s string) string {
	if len(a.words) == 0 {
		return s
	}
	keys := make([]string, 0, len(a.words))
	for k := range a.words {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	lower := asciiLower(s)
	var b strings.Builder
	changed := false
	for i := 0; i < len(s); {
		matched := false
		for _, k := range keys {
			end := i + len(k)
			if strings.HasPrefix(lower[i:], k) && leftBoundary(lower, i, k) && rightBoundary(lower, end, k) {
				b.WriteString(a.words[k])
				i, matched, changed = end, true, true
				break
			}
		}
		if !matched {
			b.WriteByte(s[i])
			i++
		}
	}
	if !changed {
		return s
	}
	return b.String()
}

// asciiLower lowercases ASCII letters only, keeping byte offsets intact.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func isWordChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool { return isDigit(c) || c >= 'a' && c <= 'f' }

// leftBoundary reports whether a match of old at s[j:] starts a token:
// not preceded by a name character or by more of a dotted number or an
// IPv6 address.
func leftBoundary(s string, j int, old string) bool {
	if j == 0 {
		return true
	}
	c := s[j-1]
	switch {
	case isWordChar(c):
		return false
	case c == '.':
		return j < 2 || !isDigit(s[j-2])
	case c == ':':
		return !strings.Contains(old, ":")
	}
	return true
}

// rightBoundary reports whether a match of old ending at s[end] ends a
// token. "192.0.2.1:80" and "host-1." at the end of a sentence match;
// "192.0.2.10" and "fe80::1:2" do not match "192.0.2.1" and "fe80::1".
func rightBoundary(s string, end int, old string) bool {
	if end >= len(s) {
		return true
	}
	c := s[end]
	next := byte(0)
	if end+1 < len(s) {
		next = s[end+1]
	}
	switch {
	case isWordChar(c):
		return false
	case c == '.':
		return !isDigit(next)
	case c == ':':
		return !strings.Contains(old, ":") || !(isHex(next) || next == ':')
	}
	return true
}
