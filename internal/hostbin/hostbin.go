// Package hostbin manages host-installed helper binaries and the detached
// processes they run: tools like juicefs and zerofs that pgcli executes
// directly on the host instead of in a container.
package hostbin

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Detect returns an existing binary on PATH (vm images may pre-install it),
// mirroring how the installers avoid redundant downloads.
func Detect(name string) (string, bool) {
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	return "", false
}

// Download describes one release asset. Member selects the file to extract
// from a .tar.gz asset; leave it empty for a bare-binary asset. SHA256 is the
// hex digest of the downloaded asset itself (the digest sidecars published
// next to GitHub release assets cover the tarball, not its contents).
type Download struct {
	Binary string
	URL    string
	Member string
	SHA256 string
}

// Ensure downloads d into dir and returns the final binary path. The asset is
// checksum-verified (when SHA256 is set), then placed atomically — direct
// rename for a bare binary, extraction to a temp name then rename for a tar
// member, so a running copy of the binary is never overwritten in place.
// Empty dir defaults to InstallDir("").
func Ensure(d Download, dir string) (string, error) {
	if d.Binary == "" || d.URL == "" {
		return "", fmt.Errorf("hostbin: Binary and URL are required")
	}
	if dir == "" {
		var err error
		dir, _, err = InstallDir("")
		if err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("hostbin: creating %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+d.Binary+".download-*")
	if err != nil {
		return "", fmt.Errorf("hostbin: temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if err := fetch(d.URL, tmp); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		return "", err
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, tmp); err != nil {
		tmp.Close()
		return "", err
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if d.SHA256 != "" && !equalHex(got, d.SHA256) {
		tmp.Close()
		return "", fmt.Errorf("hostbin: checksum mismatch for %s\n  got  %s\n  want %s", d.URL, got, d.SHA256)
	}

	final := filepath.Join(dir, d.Binary)
	if d.Member == "" {
		if err := tmp.Chmod(0o755); err != nil {
			tmp.Close()
			return "", err
		}
		if err := tmp.Close(); err != nil {
			return "", err
		}
		if err := os.Rename(tmpName, final); err != nil {
			return "", fmt.Errorf("hostbin: installing %s: %w", final, err)
		}
		return final, nil
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		return "", err
	}
	// Extract to a sibling temp name so the rename below — not the archive
	// reader — is what makes the binary visible, atomically.
	exName := tmpName + ".extracted"
	defer os.Remove(exName)
	if err := extractTarMember(tmp, d.Member, exName); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(exName, final); err != nil {
		return "", fmt.Errorf("hostbin: installing %s: %w", final, err)
	}
	return final, nil
}

// InstallDir picks where a host binary goes: preferred if writable, else
// /usr/local/bin if writable, else $HOME/.local/bin. The second return is a
// note for the user (PATH advice when we had to fall back).
func InstallDir(preferred string) (string, string, error) {
	if preferred != "" {
		if dirWritable(preferred) {
			return preferred, "", nil
		}
		return "", "", fmt.Errorf("hostbin: install dir %s is not writable (create it or pass a different --install-dir)", preferred)
	}
	if dirWritable("/usr/local/bin") {
		return "/usr/local/bin", "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("hostbin: resolving home dir: %w", err)
	}
	fallback := filepath.Join(home, ".local", "bin")
	return fallback, fmt.Sprintf("installed to %s — make sure it is on your PATH", fallback), nil
}

func dirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".pgcli-writable-probe")
	f, err := os.Create(probe)
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(probe)
	return true
}

// LatestRelease resolves the newest release tag of an org/repo via the GitHub
// API (used by --version latest).
func LatestRelease(repo string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return "", fmt.Errorf("hostbin: querying releases for %s: %w", repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("hostbin: releases API for %s returned %s", repo, resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("hostbin: decoding releases response: %w", err)
	}
	if body.TagName == "" {
		return "", fmt.Errorf("hostbin: no tag_name in releases response for %s", repo)
	}
	return body.TagName, nil
}

func fetch(url string, w io.Writer) error {
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, addr)
			},
		},
		Timeout: 300 * time.Second,
	}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("hostbin: downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hostbin: downloading %s: HTTP %s", url, resp.Status)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("hostbin: downloading %s: %w", url, err)
	}
	return nil
}

// extractTarMember pulls one member out of a gzip tar stream at r into dest.
func extractTarMember(r io.Reader, member, dest string) error {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("hostbin: not a gzip archive: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("hostbin: reading archive: %w", err)
		}
		if filepath.Base(hdr.Name) != filepath.Base(member) || hdr.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return fmt.Errorf("hostbin: writing %s: %w", dest, err)
		}
		_, cpErr := io.Copy(out, tr)
		closeErr := out.Close()
		if cpErr != nil {
			return fmt.Errorf("hostbin: extracting %s: %w", member, cpErr)
		}
		return closeErr
	}
	return fmt.Errorf("hostbin: member %s not found in archive", member)
}

func equalHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'F' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'F' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
