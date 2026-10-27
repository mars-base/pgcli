package influxdb

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseRemoteDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want RemoteOptions
	}{
		{
			name: "influxdb scheme with credentials and database",
			dsn:  "influxdb://admin:secret@db.example.com:8087/demo",
			want: RemoteOptions{Host: "db.example.com", Port: 8087, Username: "admin", Password: "secret", Database: "demo"},
		},
		{
			name: "default port and escaped database",
			dsn:  "influxdb://db.example.com/my%20db",
			want: RemoteOptions{Host: "db.example.com", Port: DefaultRemotePort, Database: "my db"},
		},
		{
			name: "https implies ssl",
			dsn:  "https://admin:secret@db.example.com/demo",
			want: RemoteOptions{Host: "db.example.com", Port: DefaultRemotePort, Username: "admin", Password: "secret", Database: "demo", SSL: true},
		},
		{
			name: "unsafe ssl query",
			dsn:  "https://db.example.com:9443?unsafe_ssl=true",
			want: RemoteOptions{Host: "db.example.com", Port: 9443, SSL: true, UnsafeSSL: true},
		},
		{
			name: "camel case unsafe ssl and ssl override",
			dsn:  "http://db.example.com?ssl=true&unsafeSsl=true",
			want: RemoteOptions{Host: "db.example.com", Port: DefaultRemotePort, SSL: true, UnsafeSSL: true},
		},
		{
			name: "ssl false overrides https scheme",
			dsn:  "https://db.example.com?ssl=false",
			want: RemoteOptions{Host: "db.example.com", Port: DefaultRemotePort},
		},
		{
			name: "ipv6 host",
			dsn:  "influxdb://[::1]:8086/demo",
			want: RemoteOptions{Host: "::1", Port: 8086, Database: "demo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRemoteDSN(tt.dsn)
			if err != nil {
				t.Fatalf("ParseRemoteDSN: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseRemoteDSN(%q) = %+v, want %+v", tt.dsn, got, tt.want)
			}
		})
	}
}

func TestParseRemoteDSNErrors(t *testing.T) {
	tests := []struct {
		dsn, wantSub string
	}{
		{"postgres://db.example.com", "unsupported"},
		{"influxdb:///demo", "no host"},
		{"influxdb://db.example.com:70000", "invalid port"},
		{"influxdb://db.example.com?bogus=1", "unknown query parameter"},
		{"http://db.example.com?ssl=maybe", "not a boolean"},
		{"http://db.example.com?unsafe_ssl=true", "without ssl"},
	}
	for _, tt := range tests {
		_, err := ParseRemoteDSN(tt.dsn)
		if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("ParseRemoteDSN(%q) error = %v, want %q", tt.dsn, err, tt.wantSub)
		}
	}
}

func TestRemoteOptionsArgsAndEnv(t *testing.T) {
	o := RemoteOptions{
		Host: "::1", Port: 8087, Username: "admin", Password: "secret",
		Database: "demo", SSL: true, UnsafeSSL: true,
	}
	wantArgs := []string{"-host", "::1", "-port", "8087", "-database", "demo", "-ssl", "-unsafeSsl"}
	if got := o.Args(); !reflect.DeepEqual(got, wantArgs) {
		t.Errorf("Args = %q, want %q", got, wantArgs)
	}
	wantEnv := []string{"INFLUX_USERNAME=admin", "INFLUX_PASSWORD=secret"}
	if got := o.Env(); !reflect.DeepEqual(got, wantEnv) {
		t.Errorf("Env = %q, want %q", got, wantEnv)
	}
	if got := o.Address(); got != "[::1]:8087" {
		t.Errorf("Address = %q, want [::1]:8087", got)
	}

	if got := (RemoteOptions{Host: "h"}).Env(); got != nil {
		t.Errorf("Env without username = %q, want nil", got)
	}
	if got := (RemoteOptions{Host: "h", Username: "admin"}).Env(); !reflect.DeepEqual(got, []string{"INFLUX_USERNAME=admin"}) {
		t.Errorf("Env without password = %q", got)
	}
}
