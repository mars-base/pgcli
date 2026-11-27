package podman

import (
	"slices"
	"strings"
	"testing"
)

func TestEtcdctlRunArgsClientCerts(t *testing.T) {
	opts := EtcdctlOpts{
		CAFile:   "/tls/ca.crt",
		CertFile: "/tls/client.crt",
		KeyFile:  "/tls/client.key",
	}
	args := etcdctlRunArgs("etcd:test", "https://127.0.0.1:2379", false, opts, []string{"member", "list"})
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"-e ETCDCTL_ENDPOINTS=https://127.0.0.1:2379",
		"-e ETCDCTL_CACERT=/etcdctl-ca/ca.crt",
		"-e ETCDCTL_CERT=/etcdctl-tls/client.crt",
		"-e ETCDCTL_KEY=/etcdctl-tls/client.key",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q\nargv: %s", want, joined)
		}
	}

	// Mounts are podman flags; after the image they would be argv for etcdctl.
	image := slices.Index(args, "etcd:test")
	for _, mount := range []string{
		"/tls/ca.crt:/etcdctl-ca/ca.crt:ro,z",
		"/tls/client.crt:/etcdctl-tls/client.crt:ro,z",
		"/tls/client.key:/etcdctl-tls/client.key:ro,z",
	} {
		i := slices.Index(args, mount)
		if i < 0 || i > image {
			t.Errorf("mount %q must precede the image (image=%d mount=%d): %s", mount, image, i, joined)
		}
	}
	if tail := args[len(args)-2:]; tail[0] != "member" || tail[1] != "list" {
		t.Errorf("etcdctl args must trail the image: %v", tail)
	}
	if strings.Contains(joined, "ETCDCTL_USER") {
		t.Errorf("unexpected user env: %s", joined)
	}
}

func TestEtcdctlRunArgsExplicitCertFlagsWin(t *testing.T) {
	opts := EtcdctlOpts{CertFile: "/tls/client.crt", KeyFile: "/tls/client.key"}
	args := etcdctlRunArgs("etcd:test", "https://127.0.0.1:2379", false, opts,
		[]string{"--cert", "/inside.crt", "--key", "/inside.key", "member", "list"})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "ETCDCTL_CERT") || strings.Contains(joined, "ETCDCTL_KEY") {
		t.Errorf("explicit etcdctl flags should suppress the mounted material: %s", joined)
	}
}
