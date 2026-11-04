// Package httpd implements the directory-listing static file server behind
// `pg http`. It serves any directory tree read-only, renders an Nginx-style
// autoindex page for directories (directories first, then case-insensitive by
// name), and never auto-serves index.html.
package httpd

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
)

//go:embed assets/index.html
var assets embed.FS

// HealthPath answers readiness probes and is reserved: a real file with this
// name is not reachable.
const HealthPath = "/__pgcli_health"

var page = template.Must(template.ParseFS(assets, "assets/index.html"))

type crumb struct {
	Name    string
	Href    string
	Current bool
}

type entry struct {
	Name    string
	Href    string
	IsDir   bool
	Size    string
	ModTime string
}

type pageData struct {
	Title      string
	Path       string
	Crumbs     []crumb
	ParentHref string
	HasParent  bool
	Entries    []entry
}

// Handler returns the read-only file server for root. Symlinks are followed,
// including ones that point outside root — the same reach an nginx autoindex
// setup gives you, and the reason the default listener is loopback-only.
func Handler(root string) http.Handler {
	fsys := os.DirFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == HealthPath {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ok")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		clean := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(clean, "/")
		if name == "" {
			name = "."
		}

		info, err := fs.Stat(fsys, name)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, fs.ErrNotExist) {
				status = http.StatusNotFound
			}
			http.Error(w, http.StatusText(status), status)
			return
		}
		if !info.IsDir() {
			http.ServeFileFS(w, r, fsys, name)
			return
		}
		serveListing(w, r, fsys, name, clean)
	})
}

func serveListing(w http.ResponseWriter, _ *http.Request, fsys fs.FS, name, clean string) {
	dirents, err := fs.ReadDir(fsys, name)
	if err != nil {
		http.Error(w, "cannot read directory", http.StatusInternalServerError)
		return
	}
	sort.Slice(dirents, func(i, j int) bool {
		di, dj := dirents[i].IsDir(), dirents[j].IsDir()
		if di != dj {
			return di
		}
		return strings.ToLower(dirents[i].Name()) < strings.ToLower(dirents[j].Name())
	})

	data := pageData{
		Title:      "Index of " + clean,
		Path:       clean,
		Crumbs:     crumbsFor(clean),
		HasParent:  clean != "/",
		ParentHref: parentHref(clean),
		Entries:    make([]entry, 0, len(dirents)),
	}
	for _, d := range dirents {
		info, err := d.Info()
		if err != nil {
			continue
		}
		rel := path.Join(clean, d.Name())
		if info.IsDir() {
			rel += "/"
		}
		e := entry{
			Name:  d.Name(),
			Href:  (&url.URL{Path: rel}).EscapedPath(),
			IsDir: info.IsDir(),
		}
		if !info.IsDir() {
			e.Size = humanSize(info.Size())
		}
		e.ModTime = info.ModTime().Format("2006-01-02 15:04")
		data.Entries = append(data.Entries, e)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := page.Execute(w, data); err != nil {
		http.Error(w, "rendering directory listing", http.StatusInternalServerError)
	}
}

// crumbsFor splits /a/b into links. The final component is marked Current so
// the template renders it as plain text.
func crumbsFor(clean string) []crumb {
	out := []crumb{{Name: "/", Href: "/"}}
	if clean == "/" {
		out[0].Current = true
		return out
	}
	acc := ""
	for _, part := range strings.Split(strings.Trim(clean, "/"), "/") {
		acc += "/" + part
		out = append(out, crumb{Name: part, Href: (&url.URL{Path: acc + "/"}).EscapedPath()})
	}
	out[len(out)-1].Current = true
	return out
}

// parentHref is the listing URL one level up; always directory-style (trailing
// slash) so relative links inside it resolve correctly.
func parentHref(clean string) string {
	parent := path.Dir(strings.TrimSuffix(clean, "/"))
	if parent == "." || parent == "/" {
		return "/"
	}
	return (&url.URL{Path: parent + "/"}).EscapedPath()
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
