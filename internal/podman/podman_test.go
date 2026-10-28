package podman

import (
	"os"
	"strconv"
	"testing"
)

func TestPIDAlive(t *testing.T) {
	if !pidAlive(strconv.Itoa(os.Getpid())) {
		t.Fatal("current process PID should be alive")
	}
	for _, pid := range []string{"", "0", "99999999"} {
		if pidAlive(pid) {
			t.Fatalf("PID %q should not be alive", pid)
		}
	}
}
