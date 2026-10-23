package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/mars-base/pgcli/internal/config"
)

func TestParseVLClusterRoles(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr string
	}{
		{name: "empty keeps existing", raw: "", want: nil},
		{name: "whitespace only", raw: "   ", want: nil},
		{name: "single", raw: "storage", want: []string{"storage"}},
		{name: "trim and dedupe", raw: " insert , select ,insert ", want: []string{"insert", "select"}},
		{name: "trailing comma ignored", raw: "storage,", want: []string{"storage"}},
		{name: "unknown role", raw: "storage,foo", wantErr: "invalid role"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVLClusterRoles(tc.raw)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseVLClusterStorageNodes(t *testing.T) {
	cases := []struct {
		name    string
		raw     []string
		want    []string
		wantErr bool
	}{
		{
			name: "ipv4",
			raw:  []string{"10.0.0.1:9432", "10.0.0.2:9432"},
			want: []string{"10.0.0.1:9432", "10.0.0.2:9432"},
		},
		{
			name: "ipv6 normalized",
			raw:  []string{"[fd00::1]:9432"},
			want: []string{"[fd00::1]:9432"},
		},
		{
			name: "trim and dedupe order preserved",
			raw:  []string{" 10.0.0.2:9432 ", "10.0.0.1:9432", "10.0.0.2:9432"},
			want: []string{"10.0.0.2:9432", "10.0.0.1:9432"},
		},
		{name: "vmcluster three-part format rejected", raw: []string{"10.0.0.1:9432:9433"}, wantErr: true},
		{name: "port zero rejected", raw: []string{"10.0.0.1:0"}, wantErr: true},
		{name: "port too large rejected", raw: []string{"10.0.0.1:65536"}, wantErr: true},
		{name: "non-numeric port rejected", raw: []string{"10.0.0.1:abc"}, wantErr: true},
		{name: "empty host rejected", raw: []string{":9432"}, wantErr: true},
		{name: "missing port rejected", raw: []string{"10.0.0.1"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVLClusterStorageNodes(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateVLClusterConfig(t *testing.T) {
	base := func() config.VLClusterConfig {
		return config.VLClusterConfig{
			ContainerPrefix: "pgcli-vlcluster-vlc",
			InsertPort:      9430,
			SelectPort:      9431,
			StoragePort:     9432,
		}
	}

	t.Run("converged", func(t *testing.T) {
		vc := base()
		if err := validateVLClusterConfig(&vc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("storage only", func(t *testing.T) {
		vc := base()
		vc.Roles = []string{"storage"}
		if err := validateVLClusterConfig(&vc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("access only without storage nodes", func(t *testing.T) {
		vc := base()
		vc.Roles = []string{"insert", "select"}
		err := validateVLClusterConfig(&vc)
		if err == nil || !strings.Contains(err.Error(), "--storage-node") {
			t.Fatalf("err = %v, want a --storage-node requirement", err)
		}
	})

	t.Run("access only with storage nodes", func(t *testing.T) {
		vc := base()
		vc.Roles = []string{"insert", "select"}
		vc.StorageNodes = []string{"10.0.0.1:9432"}
		if err := validateVLClusterConfig(&vc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("missing port", func(t *testing.T) {
		vc := base()
		vc.SelectPort = 0
		if err := validateVLClusterConfig(&vc); err == nil {
			t.Fatal("expected an error for a missing port")
		}
	})

	t.Run("duplicate ports", func(t *testing.T) {
		vc := base()
		vc.SelectPort = vc.InsertPort
		if err := validateVLClusterConfig(&vc); err == nil || !strings.Contains(err.Error(), "must differ") {
			t.Fatalf("err = %v, want a duplicate-port error", err)
		}
	})

	t.Run("only unknown roles", func(t *testing.T) {
		vc := base()
		vc.Roles = []string{"bogus"}
		if err := validateVLClusterConfig(&vc); err == nil {
			t.Fatal("expected an error for an unknown-only role list")
		}
	})
}
