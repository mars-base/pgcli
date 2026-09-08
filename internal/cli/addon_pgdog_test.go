package cli

import "testing"

func TestParsePgDogBackend(t *testing.T) {
	b, err := parsePgDogBackend("app=10.0.0.1:5432:shard1:1:primary")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Name != "app" || b.Host != "10.0.0.1" || b.Port != 5432 ||
		b.DatabaseName != "shard1" || b.Shard != 1 || b.Role != "primary" {
		t.Errorf("parsed = %+v", b)
	}
}

func TestParsePgDogBackendDefaults(t *testing.T) {
	b, err := parsePgDogBackend("app=127.0.0.1:5432:appdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Shard != 0 {
		t.Errorf("shard = %d, want 0", b.Shard)
	}
	if b.Role != "" {
		t.Errorf("role = %q, want empty (pgdog default)", b.Role)
	}
}

func TestParsePgDogBackendErrors(t *testing.T) {
	for _, s := range []string{
		"127.0.0.1:5432:appdb",       // missing NAME=
		"app=127.0.0.1:appdb",        // too few parts
		"app=127.0.0.1:notaport:app", // non-numeric port
		"app=127.0.0.1:5432:app:x",   // non-numeric shard
	} {
		if _, err := parsePgDogBackend(s); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
}

func TestParsePgDogUser(t *testing.T) {
	u, err := parsePgDogUser("alice:s3cret:app", "defaultdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Name != "alice" || u.Password != "s3cret" || u.Database != "app" {
		t.Errorf("parsed = %+v", u)
	}
}

func TestParsePgDogUserDefaultsToFirstBackend(t *testing.T) {
	u, err := parsePgDogUser("alice:s3cret", "app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Password != "s3cret" || u.Database != "app" {
		t.Errorf("parsed = %+v, want password s3cret database app", u)
	}
}

func TestParsePgDogUserPasswordWithColon(t *testing.T) {
	// A password containing ':' with an explicit trailing DBNAME: only the
	// LAST ':' splits off the database, the rest stays the password.
	u, err := parsePgDogUser("alice:a:b:c:app", "defaultdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Password != "a:b:c" || u.Database != "app" {
		t.Errorf("parsed = %+v, want password a:b:c database app", u)
	}
}

func TestParsePgDogShardedTable(t *testing.T) {
	st, err := parsePgDogShardedTable("app:users:id:bigint")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.Database != "app" || st.Name != "users" || st.Column != "id" || st.DataType != "bigint" {
		t.Errorf("parsed = %+v", st)
	}
}

func TestParsePgDogShardedTableError(t *testing.T) {
	if _, err := parsePgDogShardedTable("app:users:id"); err == nil {
		t.Error("expected error for missing data_type")
	}
}
