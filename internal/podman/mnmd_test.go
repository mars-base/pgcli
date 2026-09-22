package podman

import (
	"strings"
	"testing"
)

func TestEndpointExportPath(t *testing.T) {
	cases := []struct {
		ep, want string
	}{
		{"http://10.0.0.1:9000/data2", "/data2"},
		{"https://h:443/data10", "/data10"},
		{"http://10.0.0.1:9000", ""},
		{"http://10.0.0.1:9000/", "/"},
		{"10.0.0.1:9000/data1", "/data1"}, // scheme omitted still splits at the first slash
	}
	for _, tc := range cases {
		if got := endpointExportPath(tc.ep); got != tc.want {
			t.Errorf("endpointExportPath(%q) = %q, want %q", tc.ep, got, tc.want)
		}
	}
}

func TestValidateMNMDMatrix(t *testing.T) {
	// Valid 2-node × 2-drive matrix.
	valid := []string{
		"http://10.0.0.1:9000/data1", "http://10.0.0.1:9000/data2",
		"http://10.0.0.2:9000/data1", "http://10.0.0.2:9000/data2",
	}
	if err := ValidateMNMDMatrix(valid, 2, false); err != nil {
		t.Errorf("valid 2×2 matrix rejected: %v", err)
	}
	// No-op unless BOTH endpoints and drives are set (MNSD/SNMD/SNSD paths).
	if err := ValidateMNMDMatrix(nil, 4, false); err != nil {
		t.Errorf("SNMD (endpoints nil) must be a no-op: %v", err)
	}
	if err := ValidateMNMDMatrix([]string{"http://h/data"}, 0, false); err != nil {
		t.Errorf("MNSD (drives 0) must be a no-op: %v", err)
	}
	// Endpoints not a multiple of drives-per-node.
	err := ValidateMNMDMatrix([]string{
		"http://10.0.0.1:9000/data1", "http://10.0.0.1:9000/data2", "http://10.0.0.2:9000/data1",
	}, 2, false)
	if err == nil || !strings.Contains(err.Error(), "multiple of") {
		t.Errorf("3 endpoints ÷ 2 drives must be rejected, got %v", err)
	}
	// Single-host fold: endpoints equal to one node's drive count.
	err = ValidateMNMDMatrix([]string{
		"http://10.0.0.1:9000/data1", "http://10.0.0.1:9000/data2",
	}, 2, false)
	if err == nil || !strings.Contains(err.Error(), "at least one more node") {
		t.Errorf("a single-node matrix must be rejected, got %v", err)
	}
	// Bare /data (MNSD slot) instead of /dataN.
	err = ValidateMNMDMatrix([]string{
		"http://10.0.0.1:9000/data", "http://10.0.0.2:9000/data",
	}, 1, false)
	if err == nil || !strings.Contains(err.Error(), "/data1../data1") {
		t.Errorf("bare /data export path must be rejected, got %v", err)
	}
	// TLS scheme mismatch: a --tls node with a plaintext http:// endpoint (the
	// exact misconfiguration that makes MinIO FATAL at startup).
	err = ValidateMNMDMatrix([]string{
		"http://10.0.0.1:9000/data1", "http://10.0.0.1:9000/data2",
		"https://10.0.0.2:9000/data1", "https://10.0.0.2:9000/data2",
	}, 2, true)
	if err == nil || !strings.Contains(err.Error(), "plaintext http://") {
		t.Errorf("a --tls node with an http:// endpoint must be rejected, got %v", err)
	}
	// Inverse: plaintext node with an https:// endpoint.
	err = ValidateMNMDMatrix([]string{
		"https://10.0.0.1:9000/data1", "https://10.0.0.1:9000/data2",
		"https://10.0.0.2:9000/data1", "https://10.0.0.2:9000/data2",
	}, 2, false)
	if err == nil || !strings.Contains(err.Error(), "plaintext (no --tls)") {
		t.Errorf("a plaintext node with an https:// endpoint must be rejected, got %v", err)
	}
	// A fully https:// matrix on a --tls node is valid.
	if err := ValidateMNMDMatrix([]string{
		"https://10.0.0.1:9000/data1", "https://10.0.0.1:9000/data2",
		"https://10.0.0.2:9000/data1", "https://10.0.0.2:9000/data2",
	}, 2, true); err != nil {
		t.Errorf("https matrix on a --tls node must pass: %v", err)
	}
}

func TestIsDataSlot(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/data1", true},
		{"/data4", true},
		{"/data16", true},
		{"/data", false}, // bare /data is the MNSD export dir, not a drive slot
		{"/datapg", false},
		{"/data/", false},
		{"/mnt/data1", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isDataSlot(tc.path); got != tc.want {
			t.Errorf("isDataSlot(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
