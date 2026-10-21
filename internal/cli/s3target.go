package cli

import (
	"fmt"
	"net"
	"time"

	"github.com/mars-base/pgcli/internal/config"
)

// s3Target is a resolved view over a pgcli-managed S3 addon (minio or silo —
// field-for-field twins), reduced to what the JuiceFS/ZeroFS installers need:
// an endpoint URL for bucket operations, credentials, and TLS state. Purely
// read-only: pgcli never provisions buckets or accounts here (that is manual
// `pg mc` work); installers only check the addon exists and answers.
type s3Target struct {
	Kind     string // "minio" or "silo"
	Name     string
	BaseURL  string // scheme://listen:apiPort (no bucket path)
	User     string
	Password string
	TLS      bool
}

// resolveS3Target looks up the addon in the config, then probes the S3 API
// port. The probe is a TCP dial on the resolved listen host — the install
// fails early with "start it first" instead of hanging inside juicefs format
// or a zerofs server loop.
func resolveS3Target(cfg *config.Config, kind, name string) (*s3Target, error) {
	var listen string
	var apiPort int
	var user, pass string
	var tls bool
	switch kind {
	case "minio":
		mc, ok := cfg.Addons.Minio[name]
		if !ok {
			return nil, fmt.Errorf("minio addon %q not installed (pg addon install minio --name %s)", name, name)
		}
		listen, apiPort, user, pass, tls = mc.Listen, mc.APIPort, mc.RootUser, mc.RootPassword, mc.TLS
	case "silo":
		sc, ok := cfg.Addons.Silo[name]
		if !ok {
			return nil, fmt.Errorf("silo addon %q not installed (pg addon install silo --name %s)", name, name)
		}
		listen, apiPort, user, pass, tls = sc.Listen, sc.APIPort, sc.RootUser, sc.RootPassword, sc.TLS
	default:
		return nil, fmt.Errorf("unknown S3 addon kind %q (want minio or silo)", kind)
	}
	if apiPort == 0 {
		return nil, fmt.Errorf("%s addon %q has no API port — reinstall or fix the config", kind, name)
	}
	scheme := "http"
	if tls {
		scheme = "https"
	}
	// A wildcard bind is dialed via loopback (that is how pgcli's own S3
	// users reach the addon); a specific bind address is dialled as-is.
	probeHost := "127.0.0.1"
	if listen != "" && listen != "0.0.0.0" && listen != "::" {
		probeHost = listen
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(probeHost, fmt.Sprint(apiPort)), 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%s addon %q is not answering on %s:%d — start it first: pg addon start %s",
			kind, name, probeHost, apiPort, name)
	}
	conn.Close()
	return &s3Target{
		Kind:     kind,
		Name:     name,
		BaseURL:  fmt.Sprintf("%s://%s:%d", scheme, probeHost, apiPort),
		User:     user,
		Password: pass,
		TLS:      tls,
	}, nil
}
