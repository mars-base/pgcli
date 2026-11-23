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
	"syscall"
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
	Title       string
	Path        string
	Crumbs      []crumb
	ParentHref  string
	HasParent   bool
	AllowDelete bool
	Entries     []entry
}

// Options configures optional behavior of the file server.
type Options struct {
	// AllowDelete enables HTTP DELETE for individual files (never
	// directories) and renders a delete button after copy on each file row.
	AllowDelete bool
}

// Handler returns the read-only file server for root. Symlinks are followed,
// including ones that point outside root — the same reach an nginx autoindex
// setup gives you, and the reason the default listener is loopback-only.
func Handler(root string) http.Handler {
	return HandlerWithOptions(root, Options{})
}

// HandlerWithOptions is Handler with optional features turned on. Deletion is
// confined to root by os.Root: a symlinked intermediate directory that points
// outside root is rejected, and a symlink entry is unlinked itself, never the
// file it targets.
func HandlerWithOptions(root string, opts Options) http.Handler {
	fsys := os.DirFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)
		if clean == HealthPath {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ok")
			return
		}

		switch r.Method {
		case http.MethodGet, http.MethodHead:
		case http.MethodDelete:
			if !opts.AllowDelete {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			handleDelete(w, root, fsys, clean, r.URL.Path)
			return
		default:
			allow := "GET, HEAD"
			if opts.AllowDelete {
				allow = "GET, HEAD, DELETE"
			}
			w.Header().Set("Allow", allow)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

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
		serveListing(w, fsys, name, clean, opts.AllowDelete)
	})
}

// handleDelete unlinks one file beneath root. Directories are refused so an
// empty directory is never removed, and a symlink to a directory is refused
// too, matching the listing (which offers delete only on files).
func handleDelete(w http.ResponseWriter, root string, fsys fs.FS, clean, requestPath string) {
	name := strings.TrimPrefix(clean, "/")
	if name == "" {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "cannot delete a directory", http.StatusMethodNotAllowed)
		return
	}
	rfs, err := os.OpenRoot(root)
	if err != nil {
		http.Error(w, "cannot open served directory", http.StatusInternalServerError)
		return
	}
	defer rfs.Close()

	info, err := rfs.Lstat(name)
	if err != nil {
		status := deleteErrorStatus(err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	// A symlink is unlinked itself, never its target, so the type check below
	// uses Lstat — except that a symlink to a directory is served (and listed)
	// as a directory, so DELETE must refuse it the same way.
	isDir := info.IsDir()
	if !isDir && info.Mode()&fs.ModeSymlink != 0 {
		if target, err := fs.Stat(fsys, name); err == nil && target.IsDir() {
			isDir = true
		}
	}
	if isDir {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "cannot delete a directory", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasSuffix(requestPath, "/") {
		// "/file/" addresses a directory entry that does not exist.
		http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	if err := rfs.Remove(name); err != nil {
		status := deleteErrorStatus(err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteErrorStatus maps a removal failure to a status. os.Root reports a path
// resolving outside the root through an unexported sentinel whose text is the
// only way to recognize it.
func deleteErrorStatus(err error) int {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return http.StatusNotFound
	case errors.Is(err, fs.ErrPermission), strings.Contains(err.Error(), "path escapes from parent"):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

func serveListing(w http.ResponseWriter, fsys fs.FS, name, clean string, allowDelete bool) {
	dirents, err := fs.ReadDir(fsys, name)
	if err != nil {
		http.Error(w, "cannot read directory", http.StatusInternalServerError)
		return
	}

	entries := make([]entry, 0, len(dirents))
	for _, d := range dirents {
		info, err := d.Info()
		if err != nil {
			continue
		}
		isDir := info.IsDir()
		if !isDir && info.Mode()&fs.ModeSymlink != 0 {
			// d.Info reports the link itself; resolve it so a symlink to a
			// directory is rendered (and sorted) like the directory GET
			// navigates into. Dangling links stay files.
			if target, err := fs.Stat(fsys, path.Join(name, d.Name())); err == nil && target.IsDir() {
				isDir = true
			}
		}
		rel := path.Join(clean, d.Name())
		if isDir {
			rel += "/"
		}
		e := entry{
			Name:  d.Name(),
			Href:  (&url.URL{Path: rel}).EscapedPath(),
			IsDir: isDir,
		}
		if !isDir {
			e.Size = humanSize(info.Size())
		}
		e.ModTime = info.ModTime().Format("2006-01-02 15:04")
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	data := pageData{
		Title:       "Index of " + clean,
		Path:        clean,
		Crumbs:      crumbsFor(clean),
		HasParent:   clean != "/",
		ParentHref:  parentHref(clean),
		AllowDelete: allowDelete,
		Entries:     entries,
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
