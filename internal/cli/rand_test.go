package cli

import (
	"strings"
	"testing"
)

func TestRandDefaultLength(t *testing.T) {
	flag := randCmd.Flags().Lookup("length")
	if flag == nil {
		t.Fatal("rand command has no --length flag")
	}
	if flag.DefValue != "24" {
		t.Errorf("--length default = %q, want 24", flag.DefValue)
	}
}

func TestValidateRandLength(t *testing.T) {
	for _, n := range []int{1, randDefaultLength, randMaxLength} {
		if err := validateRandLength(n); err != nil {
			t.Errorf("validateRandLength(%d) = %v, want nil", n, err)
		}
	}
	for _, n := range []int{0, -1, randMaxLength + 1} {
		if err := validateRandLength(n); err == nil {
			t.Errorf("validateRandLength(%d) = nil, want error", n)
		}
	}
}

func TestValidateRandCount(t *testing.T) {
	for _, n := range []int{1, randDefaultCount, randMaxCount} {
		if err := validateRandCount(n); err != nil {
			t.Errorf("validateRandCount(%d) = %v, want nil", n, err)
		}
	}
	for _, n := range []int{0, -1, randMaxCount + 1} {
		if err := validateRandCount(n); err == nil {
			t.Errorf("validateRandCount(%d) = nil, want error", n)
		}
	}
}

func TestBuildRandCharset(t *testing.T) {
	for name, want := range randCharsets {
		got, err := buildRandCharset(name, "")
		if err != nil {
			t.Errorf("buildRandCharset(%q) error: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("buildRandCharset(%q) = %q, want %q", name, got, want)
		}
	}

	got, err := buildRandCharset("digits", "abc!!")
	if err != nil {
		t.Fatalf("buildRandCharset with extras: %v", err)
	}
	if got != "0123456789abc!" {
		t.Errorf("buildRandCharset with extras = %q, want duplicates removed as \"0123456789abc!\"", got)
	}

	if _, err := buildRandCharset("nope", ""); err == nil {
		t.Error("buildRandCharset with unknown name = nil error, want error")
	}
	if _, err := buildRandCharset("alnum", "密码"); err == nil {
		t.Error("buildRandCharset with non-ASCII extras = nil error, want error")
	}
	if _, err := buildRandCharset("alnum", "a b"); err == nil {
		t.Error("buildRandCharset with space in extras = nil error, want error")
	}
}

func TestRandomString(t *testing.T) {
	for name, charset := range randCharsets {
		const length = 512
		s, err := randomString(charset, length)
		if err != nil {
			t.Errorf("randomString(%q) error: %v", name, err)
			continue
		}
		if len(s) != length {
			t.Errorf("randomString(%q) length = %d, want %d", name, len(s), length)
		}
		for _, r := range s {
			if !strings.ContainsRune(charset, r) {
				t.Errorf("randomString(%q) produced %q outside its charset", name, r)
				break
			}
		}
	}

	// Different calls must not repeat the same output.
	a, _ := randomString(randCharsetAlnum, 32)
	b, _ := randomString(randCharsetAlnum, 32)
	if a == b {
		t.Error("two random strings are identical")
	}

	if _, err := randomString("", 8); err == nil {
		t.Error("randomString with empty charset = nil error, want error")
	}
}

func TestGeneratePassword(t *testing.T) {
	s, err := generatePassword(defaultPasswordLength)
	if err != nil {
		t.Fatalf("generatePassword: %v", err)
	}
	if len(s) != defaultPasswordLength {
		t.Errorf("generatePassword length = %d, want %d", len(s), defaultPasswordLength)
	}
	for _, r := range s {
		if !strings.ContainsRune(randCharsetAlnum, r) {
			t.Errorf("generatePassword produced non-alnum character %q", r)
			break
		}
	}
}
