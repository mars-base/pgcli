package influxdb

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// RemoteOptions describes an InfluxDB 1.x endpoint that pgcli does not manage:
// host/port, optional credentials and database, and TLS mode. It is the
// connection half of `pg influx` in remote mode.
type RemoteOptions struct {
	Host      string
	Port      int
	Username  string
	Password  string
	Database  string
	SSL       bool
	UnsafeSSL bool
}

// DefaultRemotePort is the InfluxDB 1.x HTTP API port.
const DefaultRemotePort = 8086

// ParseRemoteDSN parses an InfluxDB 1.x connection URL:
//
//	influxdb://user:pass@host:8086/database
//	http://host:8086
//	https://user:pass@host:8086/database?unsafe_ssl=true
//
// Schemes influxdb:// and http:// mean plain HTTP; https:// implies -ssl.
// The path is the database name (one leading slash is dropped). Query keys
// ssl and unsafe_ssl override the scheme defaults. Unknown query keys are
// rejected so a typo does not silently connect without TLS.
func ParseRemoteDSN(raw string) (RemoteOptions, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return RemoteOptions{}, fmt.Errorf("invalid InfluxDB DSN %q: %w", raw, err)
	}
	var opts RemoteOptions
	switch strings.ToLower(u.Scheme) {
	case "influx", "influxdb", "http":
		opts.SSL = false
	case "https":
		opts.SSL = true
	default:
		return RemoteOptions{}, fmt.Errorf("unsupported InfluxDB DSN scheme %q (use influxdb://, http:// or https://)", u.Scheme)
	}

	opts.Host = u.Hostname()
	if opts.Host == "" {
		return RemoteOptions{}, fmt.Errorf("InfluxDB DSN %q has no host", raw)
	}
	opts.Port = DefaultRemotePort
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return RemoteOptions{}, fmt.Errorf("InfluxDB DSN %q has an invalid port %q", raw, p)
		}
		opts.Port = n
	}
	if u.User != nil {
		opts.Username = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			opts.Password = pw
		}
	}
	if path := strings.TrimPrefix(u.Path, "/"); path != "" {
		db, err := url.PathUnescape(path)
		if err != nil {
			return RemoteOptions{}, fmt.Errorf("InfluxDB DSN %q has an invalid database path: %w", raw, err)
		}
		opts.Database = db
	}
	for key, values := range u.Query() {
		if len(values) == 0 {
			continue
		}
		val := values[len(values)-1]
		switch key {
		case "ssl":
			b, err := strconv.ParseBool(val)
			if err != nil {
				return RemoteOptions{}, fmt.Errorf("InfluxDB DSN %q: ssl=%q is not a boolean", raw, val)
			}
			opts.SSL = b
		case "unsafe_ssl", "unsafeSsl":
			b, err := strconv.ParseBool(val)
			if err != nil {
				return RemoteOptions{}, fmt.Errorf("InfluxDB DSN %q: %s=%q is not a boolean", raw, key, val)
			}
			opts.UnsafeSSL = b
		default:
			return RemoteOptions{}, fmt.Errorf("InfluxDB DSN %q has an unknown query parameter %q (supported: ssl, unsafe_ssl)", raw, key)
		}
	}
	if opts.UnsafeSSL && !opts.SSL {
		return RemoteOptions{}, fmt.Errorf("InfluxDB DSN %q sets unsafe_ssl without ssl (unsafe_ssl only applies to HTTPS)", raw)
	}
	return opts, nil
}

// Args returns the connection flags passed to the influx CLI. Host and port
// are explicit so the defaults never depend on the container's loopback.
func (o RemoteOptions) Args() []string {
	args := []string{"-host", o.Host, "-port", strconv.Itoa(o.Port)}
	if o.Database != "" {
		args = append(args, "-database", o.Database)
	}
	if o.SSL {
		args = append(args, "-ssl")
	}
	if o.UnsafeSSL {
		args = append(args, "-unsafeSsl")
	}
	return args
}

// Env returns credential environment entries for the influx CLI. They are
// passed to the container by name, so the password never appears in argv.
func (o RemoteOptions) Env() []string {
	if o.Username == "" {
		return nil
	}
	env := []string{"INFLUX_USERNAME=" + o.Username}
	if o.Password != "" {
		env = append(env, "INFLUX_PASSWORD="+o.Password)
	}
	return env
}

// Address returns host:port for display.
func (o RemoteOptions) Address() string {
	return net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
}
