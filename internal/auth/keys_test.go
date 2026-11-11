package auth

import (
	"strings"
	"testing"
)

func TestGeneratedKeyFormat(t *testing.T) {
	seen := map[string]bool{}
	chars := map[rune]bool{}
	for i := 0; i < 2000; i++ {
		key, id := generateKey()
		if len(key) != keyLen || !strings.HasPrefix(key, "ckp_"+id+"_") {
			t.Fatalf("bad key shape %d", len(key))
		}
		got, ok := parseKey(key)
		if !ok || got != id {
			t.Fatalf("generated key does not parse")
		}
		if seen[key] || seen[id] {
			t.Fatal("duplicate key or id")
		}
		seen[key], seen[id] = true, true
		for _, c := range key[len(KeyPrefix)+keyIDLen+1:] {
			chars[c] = true
		}
	}
	// With 86,000 random characters every base62 symbol must appear.
	if len(chars) != 62 {
		t.Errorf("secret alphabet coverage %d/62", len(chars))
	}
}

func TestParseKeyRejects(t *testing.T) {
	good, _ := generateKey()
	bad := []string{
		"",
		good[:len(good)-1],
		good + "x",
		"ckx_" + good[4:],
		strings.Replace(good, "_", "-", 2),
		good[:4] + "ABCDEFGHIJKL" + good[16:], // uppercase id
		good[:len(good)-1] + "!",              // non-base62 secret
		good[:len(good)-1] + "é",
	}
	for _, k := range bad {
		if _, ok := parseKey(k); ok {
			t.Errorf("accepted %q", k)
		}
	}
}

func TestHashIsSHA256(t *testing.T) {
	if h := hashKey("abc"); len(h) != 32 {
		t.Fatal("not 32 bytes")
	}
	a, _ := generateKey()
	b, _ := generateKey()
	if string(hashKey(a)) == string(hashKey(b)) {
		t.Fatal("hash collision")
	}
}

func FuzzParseKey(f *testing.F) {
	k, _ := generateKey()
	f.Add(k)
	f.Add("ckp_")
	f.Fuzz(func(t *testing.T, s string) {
		if id, ok := parseKey(s); ok && (len(id) != keyIDLen || len(s) != keyLen) {
			t.Fatalf("accepted malformed key")
		}
	})
}
