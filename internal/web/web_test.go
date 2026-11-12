package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func serve(t *testing.T, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, req)
	return w
}

func TestServesPageWithSecurityHeaders(t *testing.T) {
	for _, p := range []string{"/ui/", "/ui/index.html"} {
		w := serve(t, http.MethodGet, p, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", p, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("%s: content type %q", p, ct)
		}
		if !strings.Contains(w.Body.String(), `<script type="module" src="app.js">`) {
			t.Errorf("%s: not the dashboard page", p)
		}
		for k, want := range map[string]string{
			"Content-Security-Policy": CSP,
			"X-Content-Type-Options":  "nosniff",
			"Referrer-Policy":         "no-referrer",
			"X-Frame-Options":         "DENY",
			"Cache-Control":           "no-cache",
		} {
			if got := w.Header().Get(k); got != want {
				t.Errorf("%s: %s = %q, want %q", p, k, got, want)
			}
		}
	}
}

func TestCSPIsStrict(t *testing.T) {
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "*", "http:", "https:"} {
		if strings.Contains(CSP, bad) {
			t.Errorf("CSP allows %q", bad)
		}
	}
	for _, need := range []string{"default-src 'none'", "frame-ancestors 'none'", "connect-src 'self'", "script-src 'self'"} {
		if !strings.Contains(CSP, need) {
			t.Errorf("CSP lacks %q", need)
		}
	}
}

func TestContentTypesAndRevalidation(t *testing.T) {
	for p, ct := range map[string]string{
		"/ui/app.js":    "text/javascript; charset=utf-8",
		"/ui/style.css": "text/css; charset=utf-8",
		"/ui/icon.svg":  "image/svg+xml",
	} {
		w := serve(t, http.MethodGet, p, nil)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != ct {
			t.Errorf("%s: %d %q", p, w.Code, w.Header().Get("Content-Type"))
		}
		etag := w.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: no ETag", p)
		}
		if w := serve(t, http.MethodGet, p, map[string]string{"If-None-Match": etag}); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Errorf("%s: revalidation got %d with %d bytes", p, w.Code, w.Body.Len())
		}
		if w := serve(t, http.MethodHead, p, nil); w.Code != http.StatusOK || w.Body.Len() != 0 {
			t.Errorf("%s: HEAD got %d with %d bytes", p, w.Code, w.Body.Len())
		}
	}
}

func TestUnknownPathsAreNotFound(t *testing.T) {
	for _, p := range []string{"/ui/nope.js", "/ui/../web.go", "/ui/static/app.js", "/ui/app.js/"} {
		w := serve(t, http.MethodGet, p, nil)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", p, w.Code)
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: 404 without CSP", p)
		}
	}
}

// Every file the page or scripts reference is embedded.
func TestReferencedFilesExist(t *testing.T) {
	ref := regexp.MustCompile(`(?:src|href)="([^"]+)"|from '\./([^']+)'`)
	err := fs.WalkDir(static, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := static.ReadFile(p)
		for _, m := range ref.FindAllStringSubmatch(string(b), -1) {
			name := m[1] + m[2]
			if strings.HasPrefix(name, "#") {
				continue
			}
			if w := serve(t, http.MethodGet, Prefix+name, nil); w.Code != http.StatusOK {
				t.Errorf("%s references %s, which is not served (%d)", p, name, w.Code)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// API text must never be parsed as HTML, and the page loads nothing from
// other sites (D35).
func TestScriptsNeverInsertHTML(t *testing.T) {
	forbidden := regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|\beval\(|new Function|setAttribute\('on|https?://`)
	err := fs.WalkDir(static, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".js") && !strings.HasSuffix(p, ".html") && !strings.HasSuffix(p, ".css") {
			return err
		}
		b, _ := static.ReadFile(p)
		for i, line := range strings.Split(string(b), "\n") {
			if m := forbidden.FindString(line); m != "" {
				t.Errorf("%s:%d uses %q", p, i+1, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
