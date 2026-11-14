// Package web holds the browser dashboard (D35): static HTML, CSS and
// JavaScript embedded in the binary. The page is an API client like the
// TUI; it holds no data and needs no key to load. It calls the API from
// the browser with the key the user enters.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed static
var static embed.FS

// Prefix is the URL path the dashboard is served under.
const Prefix = "/ui/"

// CSP allows only same-origin scripts, styles and API calls, and no
// framing. Nothing inline, nothing from another site. API calls may also
// go to the configured machines (D45); see Options.
const CSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Machine is another agent the dashboard can show (D45).
type Machine struct {
	Name string `json:"name"`
	URL  string `json:"url"` // an exact origin, validated by the configuration
}

// Options configures the dashboard.
type Options struct {
	// Machines are other agents the page may call. Their origins are
	// added to the CSP's connect-src, and the list is served as
	// machines.json. Names and addresses are therefore visible to anyone
	// who can load the page, like the page itself.
	Machines []Machine
}

// csp returns CSP with the machines' origins allowed for API calls.
func csp(ms []Machine) string {
	if len(ms) == 0 {
		return CSP
	}
	connect := "connect-src 'self'"
	for _, m := range ms {
		connect += " " + m.URL
	}
	return strings.Replace(CSP, "connect-src 'self'", connect, 1)
}

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".svg":  "image/svg+xml",
}

type file struct {
	body        []byte
	contentType string
	etag        string
}

// Handler serves the dashboard files under Prefix. Unknown paths get 404;
// "" and "index.html" both serve the page.
func Handler(o Options) http.Handler {
	files := map[string]file{}
	err := fs.WalkDir(static, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ct, ok := contentTypes[path.Ext(p)]
		if !ok {
			return nil
		}
		b, err := static.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		files[strings.TrimPrefix(p, "static/")] = file{body: b, contentType: ct, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		return nil
	})
	if err != nil {
		panic("web: embedded files: " + err.Error()) // fixed at build time
	}
	machines := o.Machines
	if machines == nil {
		machines = []Machine{}
	}
	list, _ := json.Marshal(machines)
	sum := sha256.Sum256(list)
	files["machines.json"] = file{body: list, contentType: "application/json", etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
	policy := csp(o.Machines)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", policy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		name := strings.TrimPrefix(r.URL.Path, Prefix)
		if name == "" {
			name = "index.html"
		}
		f, ok := files[name]
		if !ok {
			h.Set("Content-Type", "text/plain; charset=utf-8")
			h.Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("Not found.\n"))
			return
		}
		// Revalidate every time, so an upgraded agent serves new files.
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", f.etag)
		if match := r.Header.Get("If-None-Match"); match != "" && match == f.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Type", f.contentType)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(f.body)
	})
}
