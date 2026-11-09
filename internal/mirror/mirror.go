// Package mirror redirects external download URLs through a local HTTP
// mirror for air-gapped installs.
package mirror

import (
	"net/url"
	"os"
	"path"
	"strings"
)

// Env names the environment variable that holds the mirror base URL, for
// example http://10.0.0.5:18080. When it is set, Resolve replaces an
// http(s) asset URL with <base>/<basename>.
const Env = "PGCLI_DOWNLOAD_MIRROR"

// Resolve returns raw rewritten through the configured mirror. It is a no-op
// when the variable is unset, the URL is not http(s), or the URL already
// points at the mirror.
func Resolve(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(Env)), "/")
	if base == "" || raw == "" || strings.HasPrefix(raw, base+"/") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return raw
	}
	name := path.Base(u.Path)
	if name == "" || name == "." || name == "/" {
		return raw
	}
	return base + "/" + name
}
