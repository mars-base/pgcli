package httpd

import (
	"io"
	"net/http"
	"net/http/httptest"
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
