// Package httpd implements the directory-listing static file server behind
// `pg http`. It serves any directory tree read-only by default, renders an
// Nginx-style autoindex page for directories (directories first, then
// case-insensitive by name), and never auto-serves index.html.
package httpd

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
)

//go:embed assets/index.html
var assets embed.FS

const (
	// HealthPath answers readiness probes and is reserved: a real file with
	// this name is not reachable.
	HealthPath = "/__pgcli_health"

	// DefaultMaxUploadBytes caps one upload request, all file parts combined.
	DefaultMaxUploadBytes = 1 << 30

	// uploadTempPrefix marks in-progress uploads. These names are hidden from
	// listings, refused as upload names, and renamed over the final name only
	// once fully written.
	uploadTempPrefix = ".pgcli-upload-"

	maxFormBytes = 64 << 10
)

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
	Title          string
	Path           string
	Crumbs         []crumb
	ParentHref     string
	HasParent      bool
	AllowDelete    bool
	AllowUpload    bool
	AllowDir       bool
	MaxUploadBytes int64
	PostHref       string
	Entries        []entry
}

// Options configures optional write features of the file server. Everything is
// off by default: the server stays read-only unless a feature is requested.
type Options struct {
	// AllowDelete enables HTTP DELETE for individual files (never
	// directories) and renders a delete button after copy on each file row.
	AllowDelete bool
	// AllowUpload enables multipart POST uploads into the directory being
	// listed and renders an upload form on each listing.
	AllowUpload bool
	// AllowDir enables creating directories with a form-encoded POST of a
	// relative path and renders a "new folder" form on each listing.
	AllowDir bool
	// MaxUploadBytes caps one upload request; 0 means DefaultMaxUploadBytes.
	MaxUploadBytes int64
	// AdminIPs restricts the write modes (delete, upload, mkdir) to these
	// source addresses. Empty means every source is allowed. Non-admin
	// sources get a read-only listing and 403 on write requests.
	AdminIPs []netip.Addr
}

// Handler returns the read-only file server for root. Symlinks are followed,
// including ones that point outside root — the same reach an nginx autoindex
// setup gives you, and the reason the default listener is loopback-only.
func Handler(root string) http.Handler {
	return HandlerWithOptions(root, Options{})
}

// HandlerWithOptions is Handler with optional features turned on. Writing is
// confined to root by os.Root: a symlinked intermediate directory that points
// outside root is rejected, and a symlink entry is replaced or unlinked itself,
// never the file it targets.
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
				w.Header().Set("Allow", allowHeader(opts))
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			handleDelete(w, root, fsys, clean, r, opts)
			return
		case http.MethodPost:
			if !opts.AllowUpload && !opts.AllowDir {
				w.Header().Set("Allow", allowHeader(opts))
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			handlePost(w, root, fsys, clean, r, opts)
			return
		default:
			w.Header().Set("Allow", allowHeader(opts))
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
		serveListing(w, r, fsys, name, clean, opts)
	})
}

// allowHeader lists the methods the server accepts with the current options.
func allowHeader(opts Options) string {
	allow := "GET, HEAD"
	if opts.AllowDelete {
		allow += ", DELETE"
	}
	if opts.AllowUpload || opts.AllowDir {
		allow += ", POST"
	}
	return allow
}

// sameOrigin rejects browser requests that declare a foreign origin. POST with
// multipart/form-data is a CORS "simple request" that is sent without a
// preflight, so without this check any web page could upload into a reachable
// server. Requests without an Origin header (curl, scripts) are allowed.
func sameOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	u, err := url.Parse(origins[0])
	if err != nil || u.Host == "" {
		return false // includes the opaque "null" origin
	}
	return strings.EqualFold(u.Host, r.Host)
}

// adminAllowed reports whether the request's source address may use the write
// modes. An empty AdminIPs list allows every source. Only the direct TCP peer
// (RemoteAddr) is consulted: a reverse proxy in front of the server makes every
// request appear to come from the proxy's address.
func adminAllowed(r *http.Request, opts Options) bool {
	if len(opts.AdminIPs) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, allowed := range opts.AdminIPs {
		if allowed.Unmap() == ip {
			return true
		}
	}
	return false
}

// handleDelete unlinks one file beneath root. Directories are refused so an
// empty directory is never removed, and a symlink to a directory is refused
// too, matching the listing (which offers delete only on files).
func handleDelete(w http.ResponseWriter, root string, fsys fs.FS, clean string, r *http.Request, opts Options) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	if !adminAllowed(r, opts) {
		http.Error(w, "write access is restricted to admin IPs", http.StatusForbidden)
		return
	}
	name := strings.TrimPrefix(clean, "/")
	if name == "" {
		w.Header().Set("Allow", allowHeader(opts))
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
		w.Header().Set("Allow", allowHeader(opts))
		http.Error(w, "cannot delete a directory", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/") {
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

// handlePost writes into the addressed directory: multipart/form-data uploads
// files, application/x-www-form-urlencoded creates a directory. The directory
// is the one the listing page shows, so both operations are relative to the
// path the user is browsing.
func handlePost(w http.ResponseWriter, root string, fsys fs.FS, clean string, r *http.Request, opts Options) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	if !adminAllowed(r, opts) {
		http.Error(w, "write access is restricted to admin IPs", http.StatusForbidden)
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
		if strings.HasSuffix(r.URL.Path, "/") {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		http.Error(w, "target is not a directory", http.StatusMethodNotAllowed)
		return
	}

	ctype, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch ctype {
	case "multipart/form-data":
		if !opts.AllowUpload {
			http.Error(w, "uploads are disabled", http.StatusMethodNotAllowed)
			return
		}
		limit := opts.MaxUploadBytes
		if limit <= 0 {
			limit = DefaultMaxUploadBytes
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		handleUpload(w, root, fsys, name, clean, r, limit)
	case "application/x-www-form-urlencoded":
		if !opts.AllowDir {
			http.Error(w, "directory creation is disabled", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		handleMkdir(w, root, name, clean, r)
	default:
		http.Error(w, "unsupported content type", http.StatusUnsupportedMediaType)
	}
}

// handleUpload streams every file part to a hidden temporary file in the
// addressed directory and renames it over the final name once complete, so an
// interrupted upload never corrupts an existing file. Later parts overwrite
// earlier ones with the same name.
func handleUpload(w http.ResponseWriter, root string, fsys fs.FS, dir, clean string, r *http.Request, limit int64) {
	mr, err := r.MultipartReader()
	if err != nil {
		status := http.StatusUnsupportedMediaType
		if errors.Is(err, http.ErrMissingBoundary) {
			status = http.StatusBadRequest
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	rfs, err := os.OpenRoot(root)
	if err != nil {
		http.Error(w, "cannot open served directory", http.StatusInternalServerError)
		return
	}
	defer rfs.Close()

	var uploaded []string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeUploadError(w, err, limit)
			return
		}
		if part.FileName() == "" {
			continue // regular form field
		}
		fname, ok := cleanUploadName(part.FileName(), dir == ".")
		if !ok {
			http.Error(w, "invalid file name", http.StatusBadRequest)
			return
		}
		if err := saveUpload(rfs, fsys, dir, fname, part); err != nil {
			writeUploadError(w, err, limit)
			return
		}
		uploaded = append(uploaded, fname)
	}
	if len(uploaded) == 0 {
		http.Error(w, "no files in the upload", http.StatusBadRequest)
		return
	}
	respondCreated(w, r, map[string]any{"uploaded": uploaded}, clean)
}

// errIsDirectory refuses replacing a directory with an uploaded file.
var errIsDirectory = errors.New("target is a directory")

func saveUpload(rfs *os.Root, fsys fs.FS, dir, name string, src io.Reader) error {
	target := path.Join(dir, name)
	if info, err := rfs.Lstat(target); err == nil {
		isDir := info.IsDir()
		if !isDir && info.Mode()&fs.ModeSymlink != 0 {
			// Match the listing and DELETE: a symlink to a directory is a
			// directory. Stat follows the link even outside root; that is
			// only used to refuse, never to write through it.
			if t, err := fs.Stat(fsys, target); err == nil && t.IsDir() {
				isDir = true
			}
		}
		if isDir {
			return errIsDirectory
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	tmpName, f, err := createUploadTemp(rfs, dir)
	if err != nil {
		return err
	}
	tmpPath := path.Join(dir, tmpName)
	if _, err := io.Copy(f, src); err != nil {
		f.Close()
		rfs.Remove(tmpPath)
		return err
	}
	if err := f.Chmod(0644); err != nil {
		f.Close()
		rfs.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		rfs.Remove(tmpPath)
		return err
	}
	if err := rfs.Rename(tmpPath, target); err != nil {
		rfs.Remove(tmpPath)
		return err
	}
	return nil
}

func createUploadTemp(rfs *os.Root, dir string) (string, *os.File, error) {
	for range 10 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", nil, err
		}
		name := uploadTempPrefix + hex.EncodeToString(b[:])
		f, err := rfs.OpenFile(path.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			return name, f, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, errors.New("could not allocate a temporary upload file")
}

// cleanUploadName reduces a part's file name to one safe component. Browsers
// may send a path; only the base name is kept, matching how a form upload
// lands in the directory being listed.
func cleanUploadName(raw string, atRoot bool) (string, bool) {
	name := path.Base(strings.ReplaceAll(raw, "\\", "/"))
	if name == "" || name == "." || name == ".." || name == "/" {
		return "", false
	}
	if len(name) > 255 || strings.HasPrefix(name, uploadTempPrefix) {
		return "", false
	}
	if atRoot && name == "__pgcli_health" {
		return "", false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return name, true
}

// handleMkdir creates a directory from a relative path, plus any missing
// parents. Existing targets are reported as a conflict rather than silently
// succeeding, so the page never claims a creation that did not happen.
func handleMkdir(w http.ResponseWriter, root, dir, clean string, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "cannot parse form", http.StatusBadRequest)
		return
	}
	rel, ok := cleanDirPath(r.PostFormValue("dir"))
	if !ok {
		http.Error(w, "invalid directory path", http.StatusBadRequest)
		return
	}
	if dir == "." && (rel == "__pgcli_health" || strings.HasPrefix(rel, "__pgcli_health/")) {
		http.Error(w, "directory name is reserved", http.StatusBadRequest)
		return
	}
	rfs, err := os.OpenRoot(root)
	if err != nil {
		http.Error(w, "cannot open served directory", http.StatusInternalServerError)
		return
	}
	defer rfs.Close()

	full := path.Join(dir, rel)
	if _, err := rfs.Lstat(full); err == nil {
		http.Error(w, "already exists", http.StatusConflict)
		return
	} else if !errors.Is(err, fs.ErrNotExist) {
		status := uploadErrorStatus(err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	if err := rfs.MkdirAll(full, 0755); err != nil {
		status := uploadErrorStatus(err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	respondCreated(w, r, map[string]string{"created": rel}, clean)
}

// cleanDirPath validates a user-supplied directory path: relative to the
// addressed directory, no absolute paths, no "..", no empty components.
func cleanDirPath(raw string) (string, bool) {
	p := strings.TrimSpace(raw)
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimRight(p, "/")
	if p == "" || strings.HasPrefix(p, "/") {
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	return p, true
}

// uploadErrorStatus maps a write failure to a status. os.Root reports a path
// resolving outside the root through an unexported sentinel whose text is the
// only way to recognize it.
func uploadErrorStatus(err error) int {
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxErr):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, errIsDirectory):
		return http.StatusConflict
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return http.StatusNotFound
	case errors.Is(err, fs.ErrPermission), strings.Contains(err.Error(), "path escapes from parent"):
		return http.StatusForbidden
	case errors.Is(err, syscall.ENOSPC):
		return http.StatusInsufficientStorage
	case errors.Is(err, syscall.ENAMETOOLONG), errors.Is(err, syscall.EINVAL),
		errors.Is(err, io.ErrUnexpectedEOF):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// writeUploadError reports a failed upload part. The size cap is spelled out
// so the page can tell the user which --max-upload value was exceeded.
func writeUploadError(w http.ResponseWriter, err error, limit int64) {
	status := uploadErrorStatus(err)
	if status == http.StatusRequestEntityTooLarge {
		http.Error(w, fmt.Sprintf("upload exceeds the %s limit (--max-upload)", humanSize(limit)), status)
		return
	}
	http.Error(w, http.StatusText(status), status)
}

// wantsJSON distinguishes the page's fetch calls (which reload on success)
// from plain form submissions (which follow the 303 back to the listing).
func wantsJSON(r *http.Request) bool {
	return r.Header.Get("X-Requested-With") == "fetch" ||
		strings.Contains(r.Header.Get("Accept"), "application/json")
}

func respondCreated(w http.ResponseWriter, r *http.Request, payload any, clean string) {
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(payload)
		return
	}
	http.Redirect(w, r, dirHref(clean), http.StatusSeeOther)
}

// dirHref is the listing URL for a cleaned path, always with a trailing slash.
func dirHref(clean string) string {
	if clean == "/" {
		return "/"
	}
	return (&url.URL{Path: clean + "/"}).EscapedPath()
}

func serveListing(w http.ResponseWriter, r *http.Request, fsys fs.FS, name, clean string, opts Options) {
	dirents, err := fs.ReadDir(fsys, name)
	if err != nil {
		http.Error(w, "cannot read directory", http.StatusInternalServerError)
		return
	}

	entries := make([]entry, 0, len(dirents))
	for _, d := range dirents {
		if strings.HasPrefix(d.Name(), uploadTempPrefix) {
			continue // an in-flight upload, or debris from a crash
		}
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

	admin := adminAllowed(r, opts)
	limit := opts.MaxUploadBytes
	if limit <= 0 {
		limit = DefaultMaxUploadBytes
	}
	data := pageData{
		Title:          "Index of " + clean,
		Path:           clean,
		Crumbs:         crumbsFor(clean),
		HasParent:      clean != "/",
		ParentHref:     parentHref(clean),
		AllowDelete:    opts.AllowDelete && admin,
		AllowUpload:    opts.AllowUpload && admin,
		AllowDir:       opts.AllowDir && admin,
		MaxUploadBytes: limit,
		PostHref:       dirHref(clean),
		Entries:        entries,
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
