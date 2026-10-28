// Package rabbitmq renders the managed rabbitmq.conf, the enabled_plugins
// file and the TLS material shared by the CLI and the podman manager.
package rabbitmq

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/tlsca"
)

// Container paths are fixed by the official docker.io/library/rabbitmq image.
// Only the generated files are mounted; the image's own /etc/rabbitmq/conf.d
// defaults stay untouched.
const (
	ContainerDataDir     = "/var/lib/rabbitmq"
	ContainerConfigDir   = "/etc/rabbitmq"
	ContainerConfigFile  = ContainerConfigDir + "/rabbitmq.conf"
	ContainerPluginsFile = ContainerConfigDir + "/enabled_plugins"
	ContainerTLSDir      = ContainerConfigDir + "/tls"

	// ContainerUID/ContainerGID are the image's rabbitmq user. The data dir is
	// chowned by the image entrypoint; the private key is chowned by pgcli.
	ContainerUID = 999
	ContainerGID = 999

	tlsDirName     = "tls"
	pluginsName    = "enabled_plugins"
	pluginsContent = "[rabbitmq_management].\n"
	// leaf renewal horizon, matching the other TLS addons.
	renewBefore = 30 * 24 * time.Hour
)

// BYOTLS reports whether the instance serves a user-provided certificate pair.
func BYOTLS(c config.RabbitMQConfig) bool {
	return c.TLS && c.CertFile != "" && c.KeyFile != ""
}

// ConfigDir is the host dir holding rabbitmq.conf, enabled_plugins and tls/.
func ConfigDir(c config.RabbitMQConfig) string {
	return filepath.Dir(c.ConfigPath)
}

// PluginsFile is the host path of the enabled_plugins file.
func PluginsFile(c config.RabbitMQConfig) string {
	return filepath.Join(ConfigDir(c), pluginsName)
}

// TLSDir is the host dir holding ca.crt/public.crt/private.key.
func TLSDir(c config.RabbitMQConfig) string {
	return filepath.Join(ConfigDir(c), tlsDirName)
}

// TLSCAPath is the CA certificate clients use to verify the broker.
func TLSCAPath(c config.RabbitMQConfig) string {
	return filepath.Join(TLSDir(c), tlsca.CACertFile)
}

// AMQPAddr is the plaintext AMQP endpoint.
func AMQPAddr(c config.RabbitMQConfig) string {
	return net.JoinHostPort(c.Listen, strconv.Itoa(c.AMQPPort))
}

// AMQPSAddr is the TLS AMQP endpoint (empty when TLS is off).
func AMQPSAddr(c config.RabbitMQConfig) string {
	if !c.TLS {
		return ""
	}
	return net.JoinHostPort(c.Listen, strconv.Itoa(c.AMQPTLSPort))
}

// ManagementURL is the management web UI/API base URL.
func ManagementURL(c config.RabbitMQConfig) string {
	return "http://" + net.JoinHostPort(c.Listen, strconv.Itoa(c.ManagementPort))
}

// ManagementTLSURL is the HTTPS management URL (empty when TLS is off).
func ManagementTLSURL(c config.RabbitMQConfig) string {
	if !c.TLS {
		return ""
	}
	return "https://" + net.JoinHostPort(c.Listen, strconv.Itoa(c.ManagementTLSPort))
}

// RenderConfig returns the complete rabbitmq.conf. Every listener, path and
// bootstrap-user setting that pgcli owns is rendered here; Settings may only
// add unrelated keys.
func RenderConfig(c config.RabbitMQConfig) (string, error) {
	if err := config.ValidateRabbitMQSettings(c.Settings); err != nil {
		return "", err
	}
	ip := net.ParseIP(c.Listen)
	if ip == nil {
		return "", fmt.Errorf("listen must be an IP address, got %q", c.Listen)
	}

	var b strings.Builder
	writeKey(&b, "listeners.tcp.default", net.JoinHostPort(c.Listen, strconv.Itoa(c.AMQPPort)))
	if c.TLS {
		writeKey(&b, "listeners.ssl.default", net.JoinHostPort(c.Listen, strconv.Itoa(c.AMQPTLSPort)))
		writeKey(&b, "ssl_options.cacertfile", ContainerTLSDir+"/"+tlsca.CACertFile)
		writeKey(&b, "ssl_options.certfile", ContainerTLSDir+"/"+tlsca.ServerCert)
		writeKey(&b, "ssl_options.keyfile", ContainerTLSDir+"/"+tlsca.ServerKey)
		writeKey(&b, "ssl_options.verify", "verify_none")
		writeKey(&b, "ssl_options.fail_if_no_peer_cert", "false")
	}
	b.WriteString("\n")

	writeKey(&b, "management.tcp.ip", c.Listen)
	writeKey(&b, "management.tcp.port", strconv.Itoa(c.ManagementPort))
	if c.TLS {
		writeKey(&b, "management.ssl.ip", c.Listen)
		writeKey(&b, "management.ssl.port", strconv.Itoa(c.ManagementTLSPort))
		writeKey(&b, "management.ssl.cacertfile", ContainerTLSDir+"/"+tlsca.CACertFile)
		writeKey(&b, "management.ssl.certfile", ContainerTLSDir+"/"+tlsca.ServerCert)
		writeKey(&b, "management.ssl.keyfile", ContainerTLSDir+"/"+tlsca.ServerKey)
		writeKey(&b, "management.ssl.verify", "verify_none")
		writeKey(&b, "management.ssl.fail_if_no_peer_cert", "false")
	}
	b.WriteString("\n")

	// The first boot imports RABBITMQ_DEFAULT_USER/PASS with these tags and
	// permissions; default_user/default_pass themselves never reach the file.
	writeKey(&b, "default_user_tags.administrator", "true")
	writeKey(&b, "default_permissions.configure", ".*")
	writeKey(&b, "default_permissions.read", ".*")
	writeKey(&b, "default_permissions.write", ".*")
	b.WriteString("\n")

	// Keep each instance's Erlang distribution loopback-only and pinned to its
	// own port; multi-instance hosts share the network namespace.
	writeKey(&b, "distribution.listener.interface", "127.0.0.1")
	writeKey(&b, "distribution.listener.port_range.min", strconv.Itoa(c.DistPort))
	writeKey(&b, "distribution.listener.port_range.max", strconv.Itoa(c.DistPort))

	if len(c.Settings) > 0 {
		b.WriteString("\n")
		keys := make([]string, 0, len(c.Settings))
		for key := range c.Settings {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			writeKey(&b, key, c.Settings[key])
		}
	}
	return b.String(), nil
}

func writeKey(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteString(" = ")
	b.WriteString(value)
	b.WriteByte('\n')
}

// WriteConfig atomically writes the generated rabbitmq.conf. The file holds no
// secrets (the password travels in the container env).
func WriteConfig(c config.RabbitMQConfig) error {
	content, err := RenderConfig(c)
	if err != nil {
		return err
	}
	return writeFileAtomic(c.ConfigPath, []byte(content), 0o644)
}

// WritePlugins pins the enabled plugin set to management only. Upstream
// management images may enable rabbitmq_prometheus at build time; overriding
// enabled_plugins keeps behavior deterministic and avoids a second unsecured
// listener on a fixed port shared by every instance.
func WritePlugins(c config.RabbitMQConfig) error {
	return writeFileAtomic(PluginsFile(c), []byte(pluginsContent), 0o644)
}

// WriteSecret stores the admin password in the instance secret file (0600).
func WriteSecret(c config.RabbitMQConfig) error {
	return writeFileAtomic(c.SecretPath, []byte(c.AdminPassword+"\n"), 0o600)
}

// EnsureTLSCerts generates the CA/leaf pair or installs the bring-your-own
// files under TLSDir. It is a no-op when TLS is disabled.
func EnsureTLSCerts(c config.RabbitMQConfig) error {
	if !c.TLS {
		return nil
	}
	dir := TLSDir(c)
	// The config dir is mounted into the container; the rabbitmq user must be
	// able to traverse it and read the public cert/CA. tlsca creates a missing
	// dir with 0700, so widen both explicitly.
	configDir := ConfigDir(c)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("creating config dir %s: %w", configDir, err)
	}
	if err := os.Chmod(configDir, 0o755); err != nil {
		return fmt.Errorf("setting config dir mode: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating TLS dir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return fmt.Errorf("setting TLS dir mode: %w", err)
	}

	if BYOTLS(c) {
		if err := installBYOTLS(c); err != nil {
			return err
		}
		return nil
	}

	hosts := tlsHosts(c)
	// The container password chowns private.key to uid 999, so a renewal cannot
	// overwrite it in place. Remove the leaf pair first when tlsca would renew;
	// the CA (and therefore client trust) is kept.
	if renew, err := tlsca.NeedsRenewal(dir, hosts, renewBefore); err == nil && renew {
		if err := removeIfExists(filepath.Join(dir, tlsca.ServerCert)); err != nil {
			return err
		}
		if err := removeIfExists(filepath.Join(dir, tlsca.ServerKey)); err != nil {
			return err
		}
	}
	caPath, err := tlsca.Generate(dir, hosts, renewBefore)
	if err != nil {
		return fmt.Errorf("generating RabbitMQ TLS certificates: %w", err)
	}
	if err := os.Chmod(caPath, 0o644); err != nil {
		return fmt.Errorf("setting CA cert mode: %w", err)
	}
	if err := os.Chmod(filepath.Join(dir, tlsca.ServerCert), 0o644); err != nil {
		return fmt.Errorf("setting server cert mode: %w", err)
	}
	return nil
}

// tlsHosts is the SAN set the generated certificate covers: every local
// address (host networking) plus the configured bind address when concrete.
func tlsHosts(c config.RabbitMQConfig) []string {
	var extra []string
	if ip := net.ParseIP(c.Listen); ip != nil && !ip.IsUnspecified() {
		extra = append(extra, c.Listen)
	}
	return tlsca.LocalHosts(extra...)
}

// installBYOTLS copies the operator's cert/key into the instance TLS dir under
// the names rabbitmq.conf references. The cert copy doubles as ca.crt: a
// self-signed leaf is its own trust anchor, and verify_none keeps the broker
// from requiring client certificates.
func installBYOTLS(c config.RabbitMQConfig) error {
	dir := TLSDir(c)
	cert, err := os.ReadFile(c.CertFile)
	if err != nil {
		return fmt.Errorf("reading TLS certificate %s: %w", c.CertFile, err)
	}
	key, err := os.ReadFile(c.KeyFile)
	if err != nil {
		return fmt.Errorf("reading TLS key %s: %w", c.KeyFile, err)
	}
	for _, name := range []string{tlsca.CACertFile, tlsca.ServerCert, tlsca.ServerKey} {
		if err := removeIfExists(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	if err := writeFileAtomic(filepath.Join(dir, tlsca.ServerCert), cert, 0o644); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, tlsca.CACertFile), cert, 0o644); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, tlsca.ServerKey), key, 0o600)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating dir for %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("setting mode on %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("installing %s: %w", path, err)
	}
	return nil
}
