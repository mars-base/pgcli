package podman

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValkeyCLIForwardsAuthEnvironment(t *testing.T) {
	podman := filepath.Join(t.TempDir(), "podman")
	argsFile := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nif [ \"$1\" = image ] && [ \"$2\" = exists ]; then exit 0; fi\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\n"
	if err := os.WriteFile(podman, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("REDISCLI_AUTH", "")
	t.Setenv("VALKEYCLI_AUTH", "")

	manager := &RedisManager{podman: podman}
	for _, name := range []string{"REDISCLI_AUTH", "VALKEYCLI_AUTH"} {
		t.Run(name, func(t *testing.T) {
			if err := os.Unsetenv("REDISCLI_AUTH"); err != nil {
				t.Fatal(err)
			}
			if err := os.Unsetenv("VALKEYCLI_AUTH"); err != nil {
				t.Fatal(err)
			}
			t.Setenv(name, "provided-secret")

			if err := manager.ValkeyCLI("docker.io/valkey/valkey:9.2.0", "config-secret", "127.0.0.1", 6380, false, []string{"ping"}); err != nil {
				t.Fatalf("ValkeyCLI with %s: %v", name, err)
			}
			data, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSpace(string(data)), "\n")
			if name == "VALKEYCLI_AUTH" {
				if !containsPair(args, "-e", "VALKEYCLI_AUTH=provided-secret") || !containsPair(args, "-e", "REDISCLI_AUTH=provided-secret") {
					t.Errorf("container args did not mirror VALKEYCLI_AUTH to both variables: %v", args)
				}
			} else if !containsPair(args, "-e", "REDISCLI_AUTH=provided-secret") {
				t.Errorf("container args did not forward REDISCLI_AUTH: %v", args)
			}
		})
	}
}

func TestValkeyCLIPrefersVALKEYCLIAuth(t *testing.T) {
	podman := filepath.Join(t.TempDir(), "podman")
	argsFile := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nif [ \"$1\" = image ] && [ \"$2\" = exists ]; then exit 0; fi\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\n"
	if err := os.WriteFile(podman, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGS_FILE", argsFile)
	t.Setenv("REDISCLI_AUTH", "redis-secret")
	t.Setenv("VALKEYCLI_AUTH", "valkey-secret")

	manager := &RedisManager{podman: podman}
	if err := manager.ValkeyCLI("docker.io/valkey/valkey:9.2.0", "config-secret", "127.0.0.1", 6380, false, []string{"ping"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	if !containsPair(args, "-e", "VALKEYCLI_AUTH=valkey-secret") {
		t.Errorf("container args missing VALKEYCLI_AUTH: %v", args)
	}
	if !containsPair(args, "-e", "REDISCLI_AUTH=valkey-secret") {
		t.Errorf("container args did not give VALKEYCLI_AUTH precedence: %v", args)
	}
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
