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
