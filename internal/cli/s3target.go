package cli

import (
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

// s3Target is a resolved view over an EXTERNAL S3 service (cloud or
// remote-host) — the object backend the JuiceFS/ZeroFS volumes are formatted
// against. There is no same-host pgcli-addon auto-detection: the endpoint and
// credentials are supplied explicitly at install and stored on the entry, so
// this is nothing more than the endpoint shape plus a reachability probe.
type s3Target struct {
	BaseURL string // the given scheme://host[:port], no bucket path
	TLS     bool
}

// externalS3Target parses an entry's S3Endpoint into a target and probes the
// host:port. The probe is a TCP dial of the endpoint itself — install fails
// early with a clear "not answering" instead of hanging inside `juicefs format`
// or the zerofs server's retry loop. Credentials are NOT part of the target:
// they live on the config entry, resolved from explicit flags only.
func externalS3Target(endpoint string) (*s3Target, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("no s3_endpoint — these filesystem addons mount an EXTERNAL S3 service given explicitly (--s3-endpoint http(s)://host:port)")
	}
	if err := config.ValidateS3Endpoint("s3_endpoint", endpoint); err != nil {
		return nil, err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse s3_endpoint %q: %w", endpoint, err)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("external S3 endpoint %s is not answering on %s — check the host/port and that the service is up", endpoint, net.JoinHostPort(host, port))
	}
	conn.Close()
	return &s3Target{BaseURL: endpoint, TLS: u.Scheme == "https"}, nil
}
