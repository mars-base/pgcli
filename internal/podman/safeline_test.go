package podman

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
	"github.com/mars-base/pgcli/internal/platform"
)

func safelineTestConfig(t *testing.T) config.SafelineConfig {
	t.Helper()
	return config.SafelineConfig{
		Name:              "waf1",
		Version:           config.DefaultSafelineVersion,
		PostgresVersion:   config.DefaultSafelinePostgresVersion,
		ContainerPrefix:   "pgcli-safeline-waf1",
		NetworkName:       "pgcli-safeline-waf1",
		SubnetPrefix:      "172.22.222",
		ConsolePort:       config.DefaultSafelineConsolePort,
		Listen:            "0.0.0.0",
		DataDir:           t.TempDir(),
		PostgresPassword:  "p@ss:w/rd?x#y",
		DetectorMgmtToken: "tok-abc123",
		AdminUser:         "admin",
	}
}

// argValue returns the value immediately following flag in args.
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestSafelineRolesStartOrder(t *testing.T) {
	want := []string{
		SafelineRolePostgres, SafelineRoleMgt, SafelineRoleDetector,
		SafelineRoleFvm, SafelineRoleChaos, SafelineRoleLuigi, SafelineRoleTengine,
	}
	if got := safelineRoles(); !slices.Equal(got, want) {
		t.Errorf("start order = %v, want %v", got, want)
	}
	if got := SafelineRoles(); !slices.Equal(got, want) {
		t.Errorf("SafelineRoles() = %v, want %v", got, want)
	}
}

func TestSafelineImages(t *testing.T) {
	sc := safelineTestConfig(t)
	images := safelineImages(sc)
	if len(images) != 7 {
		t.Fatalf("images = %v, want 7", images)
	}
	if images[0] != "docker.io/chaitin/safeline-postgres:15.18" {
		t.Errorf("postgres image = %q, want docker.io/chaitin/safeline-postgres:15.18", images[0])
	}
	for _, role := range []string{SafelineRoleMgt, SafelineRoleDetector, SafelineRoleTengine, SafelineRoleLuigi, SafelineRoleFvm, SafelineRoleChaos} {
		want := "docker.io/chaitin/safeline-" + role + ":9.4.7"
		if !slices.Contains(images, want) {
			t.Errorf("missing image %q in %v", want, images)
		}
	}
}

func TestSafelineCreateArgsBridge(t *testing.T) {
	sc := safelineTestConfig(t)
	cases := []struct {
		role string
		host int
	}{
		{SafelineRolePostgres, 2},
		{SafelineRoleMgt, 4},
		{SafelineRoleDetector, 5},
		{SafelineRoleLuigi, 7},
		{SafelineRoleFvm, 8},
		{SafelineRoleChaos, 10},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			args, err := safelineCreateArgs(sc, tc.role)
			if err != nil {
				t.Fatalf("safelineCreateArgs: %v", err)
			}
			if got, want := argValue(args, "--name"), "pgcli-safeline-waf1-"+tc.role; got != want {
				t.Errorf("--name = %q, want %q", got, want)
			}
			wantNet := "pgcli-safeline-waf1:ip=172.22.222." + strconv.Itoa(tc.host)
			if got := argValue(args, "--network"); got != wantNet {
				t.Errorf("--network = %q, want %q", got, wantNet)
			}
			if !slices.Contains(args, "--http-proxy=false") {
				t.Error("missing --http-proxy=false")
			}
			if got := argValue(args, "--restart"); got != "unless-stopped" {
				t.Errorf("--restart = %q, want unless-stopped", got)
			}
			if got := argValue(args, "--stop-timeout"); got != "30" {
				t.Errorf("--stop-timeout = %q, want 30", got)
			}
			if got := argValue(args, "--cap-drop"); got != "NET_RAW" {
				t.Errorf("--cap-drop = %q, want NET_RAW", got)
			}
			if !slices.Contains(args, "no-new-privileges:true") {
				t.Error("missing no-new-privileges:true")
			}
			if !slices.Contains(args, "/etc/localtime:/etc/localtime:ro") {
				t.Error("missing /etc/localtime mount")
			}
			if !slices.Contains(args, safelineImageRef(sc, tc.role)) {
				t.Errorf("missing image %q", safelineImageRef(sc, tc.role))
			}
			if tc.role != SafelineRoleMgt && slices.Contains(args, "-p") {
				t.Error("only mgt may publish a port")
			}
		})
	}
}

// TestSafelineCreateArgsFlagsPrecedeImage pins the podman argv contract: every
// option, including role-specific ones, must sit before the image reference.
// podman treats whatever follows the image as the container command, so a
// misplaced flag silently becomes an argument to the container (postgres then
// exits on the unknown option). Only postgres carries a trailing command.
func TestSafelineCreateArgsFlagsPrecedeImage(t *testing.T) {
	sc := safelineTestConfig(t)
	valueFlags := []string{"--name", "--restart", "--stop-timeout", "--network", "-v", "-e", "-p", "--cap-drop", "--security-opt", "--ulimit", "--add-host"}
	for _, role := range safelineRoles() {
		t.Run(role, func(t *testing.T) {
			args, err := safelineCreateArgs(sc, role)
			if err != nil {
				t.Fatal(err)
			}
			if args[0] != "run" {
				t.Fatalf("argv must start with run, got %q", args[0])
			}
			image := safelineImageRef(sc, role)
			idx := slices.Index(args, image)
			if idx < 0 {
				t.Fatalf("image %q missing from argv %q", image, args)
			}
			for _, flag := range valueFlags {
				pos := slices.Index(args, flag)
				if pos < 0 {
					continue
				}
				if pos+1 >= idx {
					t.Errorf("%s must precede the image at %d, found at %d in %q", flag, idx, pos, args)
				}
			}
			if !slices.Contains(args[:idx], "--http-proxy=false") {
				t.Error("--http-proxy=false must precede the image")
			}
			var wantTail []string
			if role == SafelineRolePostgres {
				wantTail = []string{"postgres", "-c", "max_connections=600"}
			}
			if tail := args[idx+1:]; !slices.Equal(tail, wantTail) {
				t.Errorf("argv after image = %q, want %q — podman would treat these as the container command", tail, wantTail)
			}
			if (role == SafelineRoleMgt) != slices.Contains(args[:idx], "-p") {
				t.Error("-p must be present for mgt and only mgt")
			}
			if role != SafelineRoleTengine && !slices.Contains(args[:idx], "--cap-drop") {
				t.Error("bridge roles must drop NET_RAW before the image")
			}
			if role != SafelineRoleTengine {
				// Upstream services talk by compose service name; the name
				// must resolve even without podman network DNS.
				for _, host := range []string{
					"safeline-postgres:172.22.222.2",
					"safeline-mgt:172.22.222.4",
					"safeline-detector:172.22.222.5",
					"safeline-luigi:172.22.222.7",
					"safeline-fvm:172.22.222.8",
					"safeline-chaos:172.22.222.10",
				} {
					if !slices.Contains(args[:idx], host) {
						t.Errorf("missing --add-host %s before the image", host)
					}
				}
			} else if slices.Contains(args[:idx], "--add-host") {
				t.Error("tengine is host-networked and must not carry --add-host entries")
			}
			if role == SafelineRoleTengine && !slices.Contains(args[:idx], "--ulimit") {
				t.Error("tengine must set --ulimit before the image")
			}
		})
	}
}

func TestSafelinePostgresArgs(t *testing.T) {
	sc := safelineTestConfig(t)
	args, err := safelineCreateArgs(sc, SafelineRolePostgres)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "POSTGRES_USER=safeline-ce") {
		t.Error("missing POSTGRES_USER=safeline-ce")
	}
	if !slices.Contains(args, "POSTGRES_PASSWORD="+sc.PostgresPassword) {
		t.Error("postgres must receive the raw password")
	}
	mount := filepath.Join(sc.DataDir, "resources", "postgres", "data") + ":/var/lib/postgresql/data:z"
	if !slices.Contains(args, mount) {
		t.Errorf("missing postgres data mount %q in %q", mount, args)
	}
	image := safelineImageRef(sc, SafelineRolePostgres)
	idx := slices.Index(args, image)
	if idx < 0 || !slices.Equal(args[idx+1:idx+4], []string{"postgres", "-c", "max_connections=600"}) {
		t.Errorf("postgres command tail wrong: %q", args[max(0, idx):])
	}
}

func TestSafelineMgtArgs(t *testing.T) {
	sc := safelineTestConfig(t)
	args, err := safelineCreateArgs(sc, SafelineRoleMgt)
	if err != nil {
		t.Fatal(err)
	}
	if got := argValue(args, "-p"); got != "0.0.0.0:9443:1443" {
		t.Errorf("-p = %q, want 0.0.0.0:9443:1443", got)
	}
	if !slices.Contains(args, "MGT_PG="+safelineDSN(sc)) {
		t.Error("missing MGT_PG DSN")
	}
	if !slices.Contains(args, "MGT_PROXY=0") {
		t.Error("missing MGT_PROXY=0")
	}
	if !slices.Contains(args, "SUBNET_PREFIX=172.22.222") {
		t.Error("missing SUBNET_PREFIX")
	}
	for _, a := range args {
		if strings.Contains(a, sc.PostgresPassword) {
			t.Errorf("raw postgres password leaked into mgt argv: %q", a)
		}
	}
	for _, mount := range []string{
		filepath.Join(sc.DataDir, "resources", "mgt") + ":/app/data:z",
		filepath.Join(sc.DataDir, "logs", "nginx") + ":/app/log/nginx:z",
		filepath.Join(sc.DataDir, "resources", "sock") + ":/app/sock",
		filepath.Join(sc.DataDir, "resources", "run") + ":/app/run",
	} {
		if !slices.Contains(args, mount) {
			t.Errorf("missing mount %q", mount)
		}
	}
}

func TestSafelineDetectorMgmtToken(t *testing.T) {
	sc := safelineTestConfig(t)
	want := "DETECTOR_MGMT_TOKEN=" + sc.DetectorMgmtToken
	for _, role := range []string{SafelineRoleDetector, SafelineRoleFvm, SafelineRoleLuigi} {
		args, err := safelineCreateArgs(sc, role)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(args, want) {
			t.Errorf("%s missing %q", role, want)
		}
	}
	for _, role := range []string{SafelineRolePostgres, SafelineRoleMgt, SafelineRoleChaos, SafelineRoleTengine} {
		args, err := safelineCreateArgs(sc, role)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(args, want) {
			t.Errorf("%s must not receive the detector management token", role)
		}
	}
}

func TestSafelineTengineArgs(t *testing.T) {
	sc := safelineTestConfig(t)
	args, err := safelineCreateArgs(sc, SafelineRoleTengine)
	if err != nil {
		t.Fatal(err)
	}
	if got := argValue(args, "--network"); got != "host" {
		t.Errorf("--network = %q, want host", got)
	}
	if slices.Contains(args, "-p") {
		t.Error("tengine must not publish fixed ports (sites configure them in the console)")
	}
	if slices.Contains(args, "--cap-drop") {
		t.Error("tengine is host-networked: upstream does not drop NET_RAW here")
	}
	if !slices.Contains(args, "no-new-privileges:true") {
		t.Error("missing no-new-privileges:true")
	}
	if got := argValue(args, "--ulimit"); got != "nofile=131072:131072" {
		t.Errorf("--ulimit = %q, want nofile=131072:131072", got)
	}
	for _, mount := range []string{
		"/etc/resolv.conf:/etc/resolv.conf:ro",
		"/dev/null:/etc/ld.so.preload:ro",
		filepath.Join(sc.DataDir, "resources", "nginx") + ":/etc/nginx:z",
		filepath.Join(sc.DataDir, "resources", "detector") + ":/resources/detector:ro",
		filepath.Join(sc.DataDir, "resources", "chaos") + ":/resources/chaos:z",
		filepath.Join(sc.DataDir, "logs", "nginx") + ":/var/log/nginx:z",
		filepath.Join(sc.DataDir, "resources", "cache") + ":/usr/local/nginx/cache:z",
		filepath.Join(sc.DataDir, "resources", "sock") + ":/app/sock",
	} {
		if !slices.Contains(args, mount) {
			t.Errorf("missing mount %q", mount)
		}
	}
	for _, env := range []string{
		"TCD_MGT_API=https://172.22.222.4:1443/api/open/publish/server",
		"SUBNET_PREFIX=172.22.222",
		"TCD_SNSERVER=172.22.222.5:8000",
		"SNSERVER_ADDR=172.22.222.5:8000",
		"CHAOS_ADDR=172.22.222.10",
	} {
		if !slices.Contains(args, env) {
			t.Errorf("missing env %q", env)
		}
	}
}

func TestSafelineCreateArgsUnknownRole(t *testing.T) {
	if _, err := safelineCreateArgs(safelineTestConfig(t), "nginx"); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestSafelineDSN(t *testing.T) {
	sc := safelineTestConfig(t)
	got := safelineDSN(sc)
	if strings.Contains(got, "p@ss:w") {
		t.Errorf("password not URL-escaped: %s", got)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	if u.Scheme != "postgres" || u.Host != "172.22.222.2" || u.Path != "/safeline-ce" || u.RawQuery != "sslmode=disable" {
		t.Errorf("DSN = %q, want postgres://…@172.22.222.2/safeline-ce?sslmode=disable", got)
	}
	if user := u.User.Username(); user != "safeline-ce" {
		t.Errorf("user = %q, want safeline-ce", user)
	}
	if pw, _ := u.User.Password(); pw != sc.PostgresPassword {
		t.Errorf("password round-trip = %q, want %q", pw, sc.PostgresPassword)
	}
}

func TestSafelineBundledPasswordRoundTrip(t *testing.T) {
	sc := safelineTestConfig(t)
	m := &SafelineManager{}
	if pw, err := m.ReadBundledPassword(&sc); err != nil || pw != "" {
		t.Fatalf("ReadBundledPassword on fresh dir = (%q, %v), want empty", pw, err)
	}
	if err := m.WriteBundledPassword(&sc, "kept-secret"); err != nil {
		t.Fatal(err)
	}
	pw, err := m.ReadBundledPassword(&sc)
	if err != nil {
		t.Fatal(err)
	}
	if pw != "kept-secret" {
		t.Errorf("round-trip = %q, want kept-secret", pw)
	}
	info, err := os.Stat(safelineBundledPasswordFile(sc))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("password file mode = %o, want 600", perm)
	}
}

func TestParseSafelineResetAdmin(t *testing.T) {
	cases := []struct {
		name       string
		out        string
		wantUser   string
		wantPasswd string
	}{
		{"ascii", "Initial username: admin\nInitial password: abcDEF123\n", "admin", "abcDEF123"},
		{"full-width colon", "Initial username：admin\nInitial password：p@ss:w0rd\n", "admin", "p@ss:w0rd"},
		{"noise and CRLF", "booting...\r\n  Initial username: admin  \r\n  Initial password:  xyz \r\n", "admin", "xyz"},
		{"ansi colors", "\x1b[1mInitial username: admin\x1b[0m\n\x1b[32mInitial password: pa55w0rd\x1b[0m\n", "admin", "pa55w0rd"},
		{"ansi trailing reset only", "Initial username: admin\nInitial password: pa55w0rd\x1b[0m\n", "admin", "pa55w0rd"},
		{"user only", "Initial username: root", "root", ""},
		{"none", "nothing to see here", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user, passwd := ParseSafelineResetAdmin(tc.out)
			if user != tc.wantUser || passwd != tc.wantPasswd {
				t.Errorf("ParseSafelineResetAdmin = (%q, %q), want (%q, %q)", user, passwd, tc.wantUser, tc.wantPasswd)
			}
		})
	}
}

func TestSafelineUnsupported(t *testing.T) {
	cases := []struct {
		name     string
		goos     platform.OS
		rootless bool
		wantSub  string
	}{
		{"macOS", platform.MacOS, false, "Linux"},
		{"rootless linux", platform.Linux, true, "rootful"},
		{"rootful linux", platform.Linux, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := safelineUnsupported(tc.goos, tc.rootless)
			if tc.wantSub == "" {
				if err != nil {
					t.Errorf("unexpected refusal: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("safelineUnsupported = %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestSafelineConsoleURL(t *testing.T) {
	sc := safelineTestConfig(t)
	if got := ConsoleURL(&sc); got != "https://127.0.0.1:9443" {
		t.Errorf("wildcard listen URL = %q, want https://127.0.0.1:9443", got)
	}
	sc.Listen = "10.0.0.5"
	if got := ConsoleURL(&sc); got != "https://10.0.0.5:9443" {
		t.Errorf("explicit listen URL = %q, want https://10.0.0.5:9443", got)
	}
	if got := safelineProbeHost(&sc); got != "10.0.0.5" {
		t.Errorf("probe host = %q, want 10.0.0.5", got)
	}
	sc.Listen = "::"
	if got := safelineProbeHost(&sc); got != "127.0.0.1" {
		t.Errorf("wildcard v6 probe host = %q, want 127.0.0.1", got)
	}
}

func TestSafelineWaitTengine(t *testing.T) {
	sc := safelineTestConfig(t)
	sockDir := filepath.Join(sc.DataDir, "resources", "sock")
	if err := os.MkdirAll(sockDir, 0o777); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(sockDir, "tcd.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	m := &SafelineManager{}
	if err := m.WaitTengine(&sc); err != nil {
		t.Fatalf("WaitTengine on a listening socket: %v", err)
	}
}

func TestSafelineEnsureFiles(t *testing.T) {
	sc := safelineTestConfig(t)
	m := &SafelineManager{}
	if err := m.ensureFiles(&sc); err != nil {
		t.Fatalf("ensureFiles: %v", err)
	}
	for _, dir := range safelineDirs(sc) {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("directory %s missing: %v", dir, err)
		}
	}
}
