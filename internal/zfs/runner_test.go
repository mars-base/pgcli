package zfs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type recordedCall struct {
	name string
	args []string
}

type fakeExecutor struct {
	calls     []recordedCall
	out       []byte
	outputErr error
	runErr    error
}

func (f *fakeExecutor) record(name string, args ...string) {
	f.calls = append(f.calls, recordedCall{name: name, args: append([]string{}, args...)})
}

func (f *fakeExecutor) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.record(name, args...)
	return f.out, f.outputErr
}

func (f *fakeExecutor) Run(_ context.Context, name string, args ...string) error {
	f.record(name, args...)
	return f.runErr
}

func (f *fakeExecutor) RunInput(_ context.Context, _ []byte, name string, args ...string) error {
	f.record(name, args...)
	return f.runErr
}

func TestRunnerPrivilegedWrapsSudo(t *testing.T) {
	exec := &fakeExecutor{out: []byte("ok")}
	r := NewTestRunner(exec, false, true)
	out, err := r.OutputPrivileged(context.Background(), "zpool", "list")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "ok" {
		t.Errorf("out = %q", out)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("calls = %d", len(exec.calls))
	}
	call := exec.calls[0]
	if call.name != "sudo" || strings.Join(call.args, " ") != "-n zpool list" {
		t.Errorf("call = %s %v", call.name, call.args)
	}
}

func TestRunnerRootDoesNotUseSudo(t *testing.T) {
	exec := &fakeExecutor{}
	r := NewTestRunner(exec, true, false)
	if !r.CanPrivilege() {
		t.Fatal("root must be privileged")
	}
	if _, err := r.OutputPrivileged(context.Background(), "zpool", "status"); err != nil {
		t.Fatal(err)
	}
	call := exec.calls[0]
	if call.name != "zpool" || strings.Join(call.args, " ") != "status" {
		t.Errorf("call = %s %v", call.name, call.args)
	}
}

func TestRunnerWithoutPrivilege(t *testing.T) {
	exec := &fakeExecutor{}
	r := NewTestRunner(exec, false, false)
	if r.CanPrivilege() {
		t.Fatal("must not be privileged")
	}
	_, err := r.OutputPrivileged(context.Background(), "zpool", "list")
	var privErr *PrivilegeError
	if !errors.As(err, &privErr) {
		t.Fatalf("err = %v, want PrivilegeError", err)
	}
	if !strings.Contains(err.Error(), "sudo pg zfs") {
		t.Errorf("error should mention sudo pg zfs: %v", err)
	}
	if len(exec.calls) != 0 {
		t.Errorf("no command should run, got %v", exec.calls)
	}
}

func TestRunnerUnprivilegedOutput(t *testing.T) {
	exec := &fakeExecutor{out: []byte("x")}
	r := NewTestRunner(exec, false, true)
	if _, err := r.Output(context.Background(), "lsblk", "-J"); err != nil {
		t.Fatal(err)
	}
	call := exec.calls[0]
	if call.name != "lsblk" {
		t.Errorf("unprivileged call wrapped: %s %v", call.name, call.args)
	}
}

func TestRunnerPrivilegedEnv(t *testing.T) {
	exec := &fakeExecutor{}
	r := NewTestRunner(exec, false, true)
	env := []string{"DEBIAN_FRONTEND=noninteractive"}
	if err := r.RunPrivilegedEnv(context.Background(), env, "apt-get", "update"); err != nil {
		t.Fatal(err)
	}
	got := exec.calls[0]
	if got.name != "sudo" {
		t.Fatalf("name = %s", got.name)
	}
	want := "-n env DEBIAN_FRONTEND=noninteractive apt-get update"
	if strings.Join(got.args, " ") != want {
		t.Errorf("args = %v, want %s", got.args, want)
	}
}

func TestVersionLineUsesStdoutDespiteExitError(t *testing.T) {
	// zfs --version prints the userland build but exits 1 when the kernel
	// module is missing; the version is still usable.
	exec := &fakeExecutor{out: []byte("zfs-2.1.11-1+deb12u1\n"), outputErr: errors.New("exit status 1")}
	r := NewTestRunner(exec, true, false)
	got, ok := r.versionLine(context.Background(), "zfs", "--version")
	if !ok || got != "zfs-2.1.11-1+deb12u1" {
		t.Errorf("versionLine = %q, %v", got, ok)
	}
}

func TestShellJoin(t *testing.T) {
	got := ShellJoin([]string{"zpool", "create", "tank", "mirror", "/dev/vdb", "/dev/vdc"})
	if got != "zpool create tank mirror /dev/vdb /dev/vdc" {
		t.Errorf("ShellJoin = %q", got)
	}
	got = ShellJoin([]string{"zfs", "set", "mountpoint=/a b", "tank/data"})
	if got != `zfs set 'mountpoint=/a b' tank/data` {
		t.Errorf("ShellJoin = %q", got)
	}
}
