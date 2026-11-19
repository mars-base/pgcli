package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func findMirror(t *testing.T) *cobra.Command {
	t.Helper()
	for _, c := range rootCmd.Commands() {
		if c.Name() == "mirror" {
			return c
		}
	}
	t.Fatal("mirror command not registered on rootCmd")
	return nil
}

func TestMirrorCommandsRegistered(t *testing.T) {
	mirror := findMirror(t)
	want := map[string]bool{"set": false, "show": false, "clean": false}
	for _, c := range mirror.Commands() {
		if _, ok := want[c.Name()]; ok {
			want[c.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("mirror subcommand %q not registered", name)
		}
	}
}

func TestMirrorArgsValidation(t *testing.T) {
	if err := mirrorSetCmd.Args(mirrorSetCmd, nil); err == nil {
		t.Error("set accepted zero arguments")
	}
	if err := mirrorSetCmd.Args(mirrorSetCmd, []string{"mirror.example.com"}); err != nil {
		t.Errorf("set rejected one argument: %v", err)
	}
	if err := mirrorSetCmd.Args(mirrorSetCmd, []string{"ghcr.io=a.example.com", "quay.io=b.example.com"}); err != nil {
		t.Errorf("set rejected two arguments: %v", err)
	}
	if err := mirrorShowCmd.Args(mirrorShowCmd, []string{"x"}); err == nil {
		t.Error("show accepted an argument")
	}
	if err := mirrorCleanCmd.Args(mirrorCleanCmd, nil); err != nil {
		t.Errorf("clean rejected zero arguments: %v", err)
	}
	if err := mirrorCleanCmd.Args(mirrorCleanCmd, []string{"docker.io", "ghcr.io"}); err != nil {
		t.Errorf("clean rejected registry arguments: %v", err)
	}
}

func TestParseMirrorArgs(t *testing.T) {
	mirrors, err := parseMirrorArgs([]string{"docker.m.daocloud.io"})
	if err != nil {
		t.Fatalf("bare host: %v", err)
	}
	if !reflect.DeepEqual(mirrors, map[string]string{"docker.io": "docker.m.daocloud.io"}) {
		t.Errorf("bare host mirrors = %v", mirrors)
	}

	mirrors, err = parseMirrorArgs([]string{"ghcr.io=ghcr.example.com", "quay.io=quay.example.com:5000"})
	if err != nil {
		t.Fatalf("pairs: %v", err)
	}
	want := map[string]string{"ghcr.io": "ghcr.example.com", "quay.io": "quay.example.com:5000"}
	if !reflect.DeepEqual(mirrors, want) {
		t.Errorf("pairs mirrors = %v", mirrors)
	}

	invalid := [][]string{
		{"foo", "ghcr.io=ghcr.example.com"},
		{"example.com=mirror.example.com"},
		{"ghcr.io="},
		{"ghcr.io=a", "ghcr.io=b"},
		{"=mirror.example.com"},
	}
	for _, args := range invalid {
		if _, err := parseMirrorArgs(args); err == nil {
			t.Errorf("parseMirrorArgs(%v) accepted invalid input", args)
		}
	}
}

func TestMirrorRejectsOutputFlag(t *testing.T) {
	flag := rootCmd.PersistentFlags().Lookup("output")
	if flag == nil {
		t.Fatal("output flag not found")
	}
	previousValue := flag.Value.String()
	previousChanged := flag.Changed
	defer func() {
		_ = flag.Value.Set(previousValue)
		flag.Changed = previousChanged
	}()

	if err := flag.Value.Set("mirror-out.yaml"); err != nil {
		t.Fatalf("set output flag: %v", err)
	}
	flag.Changed = true

	err := mirrorCmd.PersistentPreRunE(mirrorShowCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "does not use --output") {
		t.Fatalf("error = %v, want --output rejection", err)
	}
}
