package httpd

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "a.txt"), "alpha")
	mustWrite(t, filepath.Join(root, "B.txt"), "beta")
	mustWrite(t, filepath.Join(root, "sub", "b.txt"), "bravo")
	mustWrite(t, filepath.Join(root, "<img>.txt"), "escaped")
	return root
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestListingSortsDirectoriesFirst(t *testing.T) {
	h := Handler(testTree(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	sub := strings.Index(body, `href="/sub/"`)
	a := strings.Index(body, `href="/a.txt"`)
	b := strings.Index(body, `href="/B.txt"`)
	if sub < 0 || a < 0 || b < 0 {
		t.Fatalf("listing missing entries:\n%s", body)
	}
	if !(sub < a && a < b) {
		t.Errorf("order sub=%d a=%d B=%d: want directory first, then case-insensitive by name", sub, a, b)
	}
	if !strings.Contains(body, "class=\"copy\"") || !strings.Contains(body, "data-url=\"/a.txt\"") {
		t.Errorf("listing missing copy button for a.txt")
	}
	if !strings.Contains(body, "copyed") {
		t.Errorf("listing missing copyed popup text")
	}
}

func TestListingSubdirectoryAndEscaping(t *testing.T) {
	h := Handler(testTree(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub/", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `href="/sub/b.txt"`) {
		t.Fatalf("subdirectory listing = %d:\n%s", rec.Code, body)
	}
	if !strings.Contains(body, `href="/"`) {
		t.Errorf("subdirectory listing missing parent link")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body = rec.Body.String()
	if strings.Contains(body, "<img>.txt") {
		t.Errorf("file name was not HTML-escaped")
	}
	if !strings.Contains(body, "&lt;img&gt;.txt") && !strings.Contains(body, "%3Cimg%3E.txt") {
		t.Errorf("escaped file name missing from listing")
	}
}

func TestFileServingAndRange(t *testing.T) {
	h := Handler(testTree(t))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a.txt", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "alpha" {
		t.Fatalf("file = %d %q", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/a.txt", nil)
	req.Header.Set("Range", "bytes=1-3")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "lph" {
		t.Errorf("range = %d %q", rec.Code, rec.Body.String())
	}
}

func TestHealthAndMethodNotAllowed(t *testing.T) {
	h := Handler(testTree(t))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, HealthPath, nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "ok" {
		t.Errorf("health = %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}

func TestMissingPathAndTraversal(t *testing.T) {
	h := Handler(testTree(t))
	for _, p := range []string{"/nope.txt", "/../etc/passwd"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", p, rec.Code)
		}
	}
}

func TestHeadRequest(t *testing.T) {
	h := Handler(testTree(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/a.txt", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d", rec.Code)
	}
	if body, _ := io.ReadAll(rec.Body); len(body) != 0 {
		t.Errorf("HEAD returned a body: %q", body)
	}
}

func TestSubdirectoryShowsParentLink(t *testing.T) {
	h := Handler(testTree(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sub/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<a href="/">Up directory</a>`) {
		t.Errorf("subdirectory listing missing Up directory link:\n%s", body)
	}
}

func TestCrumbsAndParent(t *testing.T) {
	crumbs := crumbsFor("/a/b c")
	if len(crumbs) != 3 {
		t.Fatalf("crumbs = %+v", crumbs)
	}
	if crumbs[0].Href != "/" || crumbs[1].Href != "/a/" || crumbs[2].Href != "/a/b%20c/" {
		t.Errorf("crumb hrefs = %+v", crumbs)
	}
	if !crumbs[2].Current {
		t.Error("last crumb should be current")
	}
	if got := parentHref("/a/b/"); got != "/a/" {
		t.Errorf("parentHref = %q, want /a/", got)
	}
}

func TestDeleteDisabledByDefault(t *testing.T) {
	root := testTree(t)
	h := Handler(root)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(rec.Body.String(), `class="delete"`) {
		t.Error("listing has a delete button although deletion is off")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/a.txt", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("DELETE = %d Allow=%q, want 405 with GET, HEAD", rec.Code, rec.Header().Get("Allow"))
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Errorf("a.txt was removed although deletion is off: %v", err)
	}
}

func TestDeleteEnabledListingAndRemoval(t *testing.T) {
	root := testTree(t)
	mustWrite(t, filepath.Join(root, "my file.txt"), "spaced")
	h := HandlerWithOptions(root, Options{AllowDelete: true})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	copyIdx := strings.Index(body, `class="copy"`)
	delIdx := strings.Index(body, `class="delete"`)
	if copyIdx < 0 || delIdx < 0 || delIdx < copyIdx {
		t.Fatalf("delete button missing or not after copy (copy=%d delete=%d)", copyIdx, delIdx)
	}
	if got, want := strings.Count(body, `class="delete"`), 4; got != want {
		t.Errorf("delete buttons = %d, want %d (file rows only)", got, want)
	}
	if !strings.Contains(body, `data-name="&lt;img&gt;.txt"`) {
		t.Errorf("delete button name is not HTML-escaped:\n%s", body)
	}
	if !strings.Contains(body, "delete enabled") {
		t.Error("footer does not mention delete enabled")
	}

	for _, p := range []string{"/a.txt", "/sub/b.txt", "/my%20file.txt"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, p, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s = %d, want 204", p, rec.Code)
		}
	}
	for _, rel := range []string{"a.txt", "sub/b.txt", "my file.txt"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s still exists after DELETE", rel)
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/a.txt", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("second DELETE = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD, DELETE" {
		t.Errorf("POST = %d Allow=%q, want 405 with GET, HEAD, DELETE", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestDeleteRefusesDirectoriesAndReservedPaths(t *testing.T) {
	root := testTree(t)
	if err := os.Mkdir(filepath.Join(root, "emptydir"), 0755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "__pgcli_health"), "shadow")
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")
	mustWrite(t, outsideFile, "outside")
	h := HandlerWithOptions(root, Options{AllowDelete: true})

	cases := []struct {
		path string
		want int
	}{
		{"/", http.StatusMethodNotAllowed},
		{"/sub", http.StatusMethodNotAllowed},
		{"/sub/", http.StatusMethodNotAllowed},
		{"/emptydir", http.StatusMethodNotAllowed},
		{"/a.txt/", http.StatusNotFound},
		{"/__pgcli_health", http.StatusMethodNotAllowed},
		{"/./__pgcli_health", http.StatusMethodNotAllowed},
		{"/nope.txt", http.StatusNotFound},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("DELETE %s = %d, want %d", tc.path, rec.Code, tc.want)
		}
	}
	for _, rel := range []string{"a.txt", "sub/b.txt", "__pgcli_health", "emptydir"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s removed by a refused DELETE: %v", rel, err)
		}
	}

	// Path traversal is cleaned to a path inside root, so it is a plain 404.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/../"+filepath.Base(outside)+"/outside.txt", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("traversal DELETE = %d, want 404", rec.Code)
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Errorf("outside file removed: %v", err)
	}
}

func TestDeleteSymlinkSafety(t *testing.T) {
	root := testTree(t)
	outside := t.TempDir()
	outsideDir := filepath.Join(outside, "share")
	if err := os.Mkdir(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(outsideDir, "secret.txt"), "secret")
	links := map[string]string{
		"outlink":     outsideDir,
		"relescape":   filepath.Join("..", filepath.Base(outside)),
		"link.txt":    filepath.Join(outsideDir, "secret.txt"),
		"subdir-link": "sub",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	h := HandlerWithOptions(root, Options{AllowDelete: true})

	for _, p := range []string{"/outlink/secret.txt", "/relescape/share/secret.txt"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, p, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("DELETE %s = %d, want 403", p, rec.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "secret.txt")); err != nil {
		t.Errorf("outside file removed through a symlinked directory: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	for _, href := range []string{`href="/outlink/"`, `href="/subdir-link/"`} {
		if !strings.Contains(body, href) {
			t.Errorf("symlink to a directory not listed as a directory (%s):\n%s", href, body)
		}
	}
	for _, p := range []string{"/outlink", "/outlink/", "/subdir-link"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, p, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("DELETE %s = %d, want 405", p, rec.Code)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "outlink")); err != nil {
		t.Errorf("directory symlink was removed: %v", err)
	}

	// A symlink to a file is unlinked itself; its target survives.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/link.txt", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /link.txt = %d, want 204", rec.Code)
	}
	if _, err := os.Lstat(filepath.Join(root, "link.txt")); !os.IsNotExist(err) {
		t.Error("link.txt still exists")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "secret.txt")); err != nil {
		t.Errorf("symlink target was removed: %v", err)
	}
}

type uploadFile struct {
	name    string
	content string
}

func uploadRequest(t *testing.T, target string, files []uploadFile) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range files {
		fw, err := mw.CreateFormFile("files", f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(fw, f.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Requested-With", "fetch")
	return req
}

func formRequest(t *testing.T, target, dir string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader("dir="+url.QueryEscape(dir)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "fetch")
	return req
}

func getBody(t *testing.T, h http.Handler, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", target, rec.Code)
	}
	return rec.Body.String()
}

func noUploadTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pgcli-upload-") {
			t.Errorf("leftover upload temp file %s", e.Name())
		}
	}
}

func TestUploadDisabledByDefault(t *testing.T) {
	root := testTree(t)
	h := Handler(root)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, uploadRequest(t, "/", []uploadFile{{"new.txt", "x"}}))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST = %d Allow=%q, want 405 with GET, HEAD", rec.Code, rec.Header().Get("Allow"))
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Error("file created although uploads are off")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, formRequest(t, "/", "newdir"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("form POST = %d, want 405", rec.Code)
	}
}

func TestUploadWritesFiles(t *testing.T) {
	root := testTree(t)
	h := HandlerWithOptions(root, Options{AllowUpload: true})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, uploadRequest(t, "/", []uploadFile{
		{"up.txt", "one"},
		{"img/a.png", "png"},  // browser-supplied paths are reduced to the base name
		{"win\\b.txt", "win"}, // backslash separators too
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}
	for name, content := range map[string]string{"up.txt": "one", "a.png": "png", "b.txt": "win"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != content {
			t.Errorf("%s = %q, %v; want %q", name, data, err, content)
		}
	}
	noUploadTemps(t, root)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, uploadRequest(t, "/sub/", []uploadFile{{"c.txt", "charlie"}}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("sub upload = %d", rec.Code)
	}
	if data, err := os.ReadFile(filepath.Join(root, "sub", "c.txt")); err != nil || string(data) != "charlie" {
		t.Errorf("sub/c.txt = %q, %v", data, err)
	}
	noUploadTemps(t, filepath.Join(root, "sub"))
}

func TestUploadAtomicOverwrite(t *testing.T) {
	root := testTree(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	mustWrite(t, secret, "secret")
	if err := os.Symlink(secret, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	h := HandlerWithOptions(root, Options{AllowUpload: true})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, uploadRequest(t, "/", []uploadFile{{"a.txt", "replaced"}, {"link.txt", "direct"}}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("overwrite = %d %s", rec.Code, rec.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "replaced" {
		t.Errorf("a.txt = %q, want replaced", data)
	}
	// The symlink itself is replaced by the uploaded file; its target survives.
	info, err := os.Lstat(filepath.Join(root, "link.txt"))
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("link.txt is not a regular file after upload: %v", err)
	}
	if data, _ := os.ReadFile(secret); string(data) != "secret" {
		t.Errorf("symlink target was modified: %q", data)
	}
	noUploadTemps(t, root)
}

func TestUploadRefusesDirectories(t *testing.T) {
	root := testTree(t)
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(root, "outlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub", filepath.Join(root, "subdir-link")); err != nil {
		t.Fatal(err)
	}
	h := HandlerWithOptions(root, Options{AllowUpload: true})

	for _, name := range []string{"sub", "outlink", "subdir-link"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, uploadRequest(t, "/", []uploadFile{{name, "data"}}))
		if rec.Code != http.StatusConflict {
			t.Errorf("upload over %s = %d, want 409", name, rec.Code)
		}
	}
	noUploadTemps(t, root)
}

func TestUploadRefusesEscapes(t *testing.T) {
	root := testTree(t)
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(root, "outlink")); err != nil {
		t.Fatal(err)
	}
	h := HandlerWithOptions(root, Options{AllowUpload: true})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, uploadRequest(t, "/outlink/", []uploadFile{{"evil.txt", "evil"}}))
	if rec.Code != http.StatusForbidden {
		t.Errorf("upload through escaping symlink = %d, want 403", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "evil.txt")); !os.IsNotExist(err) {
		t.Error("file written outside the served directory")
	}
}

func TestCleanUploadName(t *testing.T) {
	for _, raw := range []string{"", ".", "..", "/", ".pgcli-upload-abc", strings.Repeat("a", 256)} {
		if name, ok := cleanUploadName(raw, false); ok {
			t.Errorf("cleanUploadName(%q) = %q, want rejected", raw, name)
		}
	}
	if name, ok := cleanUploadName("a\\b.txt", false); !ok || name != "b.txt" {
		t.Errorf("cleanUploadName(backslash) = %q, %v; want b.txt", name, ok)
	}
	if _, ok := cleanUploadName("__pgcli_health", true); ok {
		t.Error("reserved health name accepted at the root")
	}
	if name, ok := cleanUploadName("__pgcli_health", false); !ok || name != "__pgcli_health" {
		t.Errorf("health name refused in a subdirectory: %q, %v", name, ok)
	}
}

func TestUploadReservedHealthAtRootOnly(t *testing.T) {
	root := testTree(t)
	h := HandlerWithOptions(root, Options{AllowUpload: true})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, uploadRequest(t, "/", []uploadFile{{"__pgcli_health", "shadow"}}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("root health upload = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, uploadRequest(t, "/sub/", []uploadFile{{"__pgcli_health", "ok"}}))
	if rec.Code != http.StatusCreated {
		t.Errorf("subdirectory health upload = %d, want 201", rec.Code)
	}
}

func TestUploadMaxBytes(t *testing.T) {
	root := testTree(t)
	h := HandlerWithOptions(root, Options{AllowUpload: true, MaxUploadBytes: 64})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, uploadRequest(t, "/", []uploadFile{{"big.txt", strings.Repeat("x", 256)}}))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload = %d, want 413", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "--max-upload") || !strings.Contains(body, "64 B") {
		t.Errorf("413 body = %q, want the configured limit named", body)
	}
	if body := getBody(t, h, "/"); !strings.Contains(body, `data-max-upload="64"`) {
		t.Error("listing does not advertise the upload limit")
	}
	if _, err := os.Stat(filepath.Join(root, "big.txt")); !os.IsNotExist(err) {
		t.Error("oversized upload left a partial file")
	}
	noUploadTemps(t, root)
}

func TestUploadErrors(t *testing.T) {
	root := testTree(t)
	h := HandlerWithOptions(root, Options{AllowUpload: true})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
	req.Header.Set("Content-Type", "multipart/form-data")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing boundary = %d, want 400", rec.Code)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("note", "no files")
	_ = mw.Close()
	req = httptest.NewRequest(http.MethodPost, "/", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("upload without files = %d, want 400", rec.Code)
	}

	for _, target := range []string{"/a.txt", "/a.txt/", "/nope/"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, uploadRequest(t, target, []uploadFile{{"x.txt", "y"}}))
		want := http.StatusMethodNotAllowed
		if strings.HasSuffix(target, "/") {
			want = http.StatusNotFound
		}
		if rec.Code != want {
			t.Errorf("POST %s = %d, want %d", target, rec.Code, want)
		}
	}

	// Form-encoded dir creation is refused while --allow-dir is off.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, formRequest(t, "/", "newdir"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("mkdir while disabled = %d, want 405", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("plain"))
	req.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("unsupported content type = %d, want 415", rec.Code)
	}
}

func TestUploadResponses(t *testing.T) {
	root := testTree(t)
	h := HandlerWithOptions(root, Options{AllowUpload: true})

	req := uploadRequest(t, "/sub/", []uploadFile{{"r.txt", "r"}})
	req.Header.Del("X-Requested-With")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/sub/" {
		t.Errorf("plain upload = %d Location=%q, want 303 /sub/", rec.Code, rec.Header().Get("Location"))
	}

	req = uploadRequest(t, "/sub/", []uploadFile{{"j.txt", "j"}})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("fetch upload = %d, want 201", rec.Code)
	}
	var payload struct {
		Uploaded []string `json:"uploaded"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || len(payload.Uploaded) != 1 || payload.Uploaded[0] != "j.txt" {
		t.Errorf("upload JSON = %s (err %v)", rec.Body.String(), err)
	}
}

func TestDirCreate(t *testing.T) {
	root := testTree(t)
	h := HandlerWithOptions(root, Options{AllowDir: true})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, formRequest(t, "/", "images/v2"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("mkdir = %d %s", rec.Code, rec.Body.String())
	}
	if info, err := os.Stat(filepath.Join(root, "images", "v2")); err != nil || !info.IsDir() {
		t.Fatalf("images/v2 not created: %v", err)
	}

	// The path is relative to the listing being viewed.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, formRequest(t, "/sub/", "deep/nested/"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("sub mkdir = %d %s", rec.Code, rec.Body.String())
	}
	if info, err := os.Stat(filepath.Join(root, "sub", "deep", "nested")); err != nil || !info.IsDir() {
		t.Fatalf("sub/deep/nested not created: %v", err)
	}

	for _, exists := range []string{"images/v2", "a.txt"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, formRequest(t, "/", exists))
		if rec.Code != http.StatusConflict {
			t.Errorf("mkdir %s = %d, want 409", exists, rec.Code)
		}
	}

	for _, bad := range []string{"", "  ", "/abs", "..", "../x", "a/../b", "a//b", "__pgcli_health", "__pgcli_health/x"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, formRequest(t, "/", bad))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("mkdir %q = %d, want 400", bad, rec.Code)
		}
	}
}

func TestDirCreateEscape(t *testing.T) {
	root := testTree(t)
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(root, "outlink")); err != nil {
		t.Fatal(err)
	}
	h := HandlerWithOptions(root, Options{AllowDir: true})

	for _, req := range []*http.Request{
		formRequest(t, "/", "outlink/newdir"),
		formRequest(t, "/outlink/", "newdir"),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("mkdir through escaping symlink = %d, want 403", rec.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "newdir")); !os.IsNotExist(err) {
		t.Error("directory created outside the served tree")
	}
}

func TestCrossOriginWritesRefused(t *testing.T) {
	root := testTree(t)
	h := HandlerWithOptions(root, Options{AllowDelete: true, AllowUpload: true, AllowDir: true})

	req := uploadRequest(t, "/", []uploadFile{{"evil.txt", "x"}})
	req.Header.Set("Origin", "http://evil.test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin upload = %d, want 403", rec.Code)
	}

	req = formRequest(t, "/", "evil-dir")
	req.Header.Set("Origin", "null")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("null-origin mkdir = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodDelete, "/a.txt", nil)
	req.Header.Set("Origin", "http://evil.test")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin DELETE = %d, want 403", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Error("file deleted by a cross-origin DELETE")
	}

	req = uploadRequest(t, "/", []uploadFile{{"ok.txt", "x"}})
	req.Header.Set("Origin", "http://"+req.Host)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("same-origin upload = %d, want 201", rec.Code)
	}
}

func TestListingForms(t *testing.T) {
	root := testTree(t)
	if body := getBody(t, Handler(root), "/"); strings.Contains(body, `class="tool`) {
		t.Error("tool forms rendered although upload and dir are off")
	}

	h := HandlerWithOptions(root, Options{AllowUpload: true, AllowDir: true})
	body := getBody(t, h, "/sub/")
	for _, want := range []string{
		`class="tool upload"`, `enctype="multipart/form-data"`, `name="files"`,
		`data-max-upload="1073741824"`,
		`class="tool mkdir"`, `name="dir"`, `action="/sub/"`,
		"upload enabled", "mkdir enabled",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("listing missing %q", want)
		}
	}
	if strings.Contains(body, "delete enabled") {
		t.Error("footer mentions delete although it is off")
	}

	if err := os.MkdirAll(filepath.Join(root, "a b"), 0755); err != nil {
		t.Fatal(err)
	}
	if body := getBody(t, h, "/a%20b/"); !strings.Contains(body, `action="/a%20b/"`) {
		t.Errorf("form action is not the escaped listing path:\n%s", body)
	}
}

func TestListingHidesUploadTemps(t *testing.T) {
	root := testTree(t)
	mustWrite(t, filepath.Join(root, ".pgcli-upload-deadbeef"), "partial")
	if body := getBody(t, Handler(root), "/"); strings.Contains(body, ".pgcli-upload-") {
		t.Error("in-flight upload temp file is listed")
	}
}

func TestAllowHeaderComposition(t *testing.T) {
	root := testTree(t)
	h := HandlerWithOptions(root, Options{AllowDelete: true, AllowUpload: true, AllowDir: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD, DELETE, POST" {
		t.Errorf("PUT = %d Allow=%q, want 405 with GET, HEAD, DELETE, POST", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestAdminIPRestrictsWrites(t *testing.T) {
	root := testTree(t)
	h := HandlerWithOptions(root, Options{
		AllowDelete: true,
		AllowUpload: true,
		AllowDir:    true,
		AdminIPs: []netip.Addr{
			netip.MustParseAddr("10.0.0.5"),
			netip.MustParseAddr("2001:db8::7"),
		},
	})
	const outsider = "192.0.2.1:1234"

	// Non-admin sources still read, but the listing is stripped of every
	// management control.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = outsider
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("outsider listing = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, banned := range []string{`class="tool upload"`, `class="tool mkdir"`, `class="delete"`, "upload enabled", "mkdir enabled", "delete enabled"} {
		if strings.Contains(body, banned) {
			t.Errorf("outsider listing contains %q", banned)
		}
	}

	req = uploadRequest(t, "/", []uploadFile{{"evil.txt", "x"}})
	req.RemoteAddr = outsider
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("outsider upload = %d, want 403", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "evil.txt")); !os.IsNotExist(err) {
		t.Error("outsider upload wrote a file")
	}

	req = formRequest(t, "/", "evil-dir")
	req.RemoteAddr = outsider
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("outsider mkdir = %d, want 403", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "evil-dir")); !os.IsNotExist(err) {
		t.Error("outsider mkdir created a directory")
	}

	req = httptest.NewRequest(http.MethodDelete, "/a.txt", nil)
	req.RemoteAddr = outsider
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("outsider DELETE = %d, want 403", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Error("outsider DELETE removed a file")
	}

	// Listed sources get the management page and may write. Both address
	// families are matched.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:5555"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body = rec.Body.String()
	for _, want := range []string{`class="tool upload"`, `class="tool mkdir"`, `class="delete"`, "delete enabled"} {
		if !strings.Contains(body, want) {
			t.Errorf("admin listing missing %q", want)
		}
	}

	req = uploadRequest(t, "/", []uploadFile{{"ok.txt", "x"}})
	req.RemoteAddr = "[2001:db8::7]:6666"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("admin upload = %d, want 201", rec.Code)
	}

	req = formRequest(t, "/", "newdir")
	req.RemoteAddr = "10.0.0.5:5555"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("admin mkdir = %d, want 201", rec.Code)
	}

	req = httptest.NewRequest(http.MethodDelete, "/a.txt", nil)
	req.RemoteAddr = "10.0.0.5:5555"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("admin DELETE = %d, want 204", rec.Code)
	}
}

func TestAdminIPEmptyMeansUnrestricted(t *testing.T) {
	root := testTree(t)
	h := HandlerWithOptions(root, Options{AllowUpload: true})
	req := uploadRequest(t, "/", []uploadFile{{"open.txt", "x"}})
	req.RemoteAddr = "203.0.113.9:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("upload with no admin list = %d, want 201", rec.Code)
	}
}

func TestAdminIPMappedIPv4(t *testing.T) {
	root := testTree(t)
	h := HandlerWithOptions(root, Options{
		AllowUpload: true,
		AdminIPs:    []netip.Addr{netip.MustParseAddr("10.0.0.5")},
	})
	req := uploadRequest(t, "/", []uploadFile{{"mapped.txt", "x"}})
	req.RemoteAddr = "[::ffff:10.0.0.5]:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("mapped-IPv4 admin upload = %d, want 201", rec.Code)
	}
}
