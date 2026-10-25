// Package zfs implements ZFS installation, disk safety checks and the
// pool/dataset/snapshot management used by the `pg zfs` command family.
package zfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PrivilegeError is returned when a privileged operation is requested but the
// current user is neither root nor able to use passwordless sudo.
type PrivilegeError struct {
	Err error
}

func (e *PrivilegeError) Error() string {
	return fmt.Sprintf("ZFS management requires root: run \"sudo pg zfs ...\" or configure passwordless sudo for this user (%v)", e.Err)
}

func (e *PrivilegeError) Unwrap() error { return e.Err }

// CommandExecutor abstracts process execution so command construction can be
// tested without root or a running ZFS.
type CommandExecutor interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	Run(ctx context.Context, name string, args ...string) error
	RunInput(ctx context.Context, input []byte, name string, args ...string) error
}

type realExecutor struct{}

func (realExecutor) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func (realExecutor) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (realExecutor) RunInput(ctx context.Context, input []byte, name string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// Runner executes external commands, wrapping privileged ones in `sudo -n`.
// It never invokes an interactive sudo prompt.
type Runner struct {
	exec CommandExecutor
	root bool
	sudo bool
}

// NewRunner preflights privileges: root, or a working `sudo -n true`.
func NewRunner(ctx context.Context) (*Runner, error) {
	r := &Runner{exec: realExecutor{}, root: os.Geteuid() == 0}
	if r.root {
		return r, nil
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return nil, &PrivilegeError{Err: err}
	}
	// Output rather than Run: a failed probe should not print sudo's
	// "a password is required" warning before our own error message.
	if _, err := r.exec.Output(ctx, "sudo", "-n", "true"); err != nil {
		return nil, &PrivilegeError{Err: err}
	}
	r.sudo = true
	return r, nil
}

// NewReadOnlyRunner probes privileges without failing. It is used by status
// and disk listing, which can still show useful static information when the
// caller is not root.
func NewReadOnlyRunner(ctx context.Context) *Runner {
	r := &Runner{exec: realExecutor{}, root: os.Geteuid() == 0}
	if r.root {
		return r
	}
	if _, err := exec.LookPath("sudo"); err == nil {
		if _, err := r.exec.Output(ctx, "sudo", "-n", "true"); err == nil {
			r.sudo = true
		}
	}
	return r
}

// NewTestRunner is used by unit tests.
func NewTestRunner(exec CommandExecutor, root, sudo bool) *Runner {
	return &Runner{exec: exec, root: root, sudo: sudo}
}

func (r *Runner) Root() bool         { return r.root }
func (r *Runner) CanPrivilege() bool { return r.root || r.sudo }

func (r *Runner) privileged(name string, args []string) (string, []string) {
	if r.root {
		return name, args
	}
	wrapped := make([]string, 0, len(args)+2)
	wrapped = append(wrapped, "-n", name)
	wrapped = append(wrapped, args...)
	return "sudo", wrapped
}

func commandError(name string, args []string, err error) error {
	full := ShellJoin(append([]string{name}, args...))
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if msg := strings.TrimSpace(string(exitErr.Stderr)); msg != "" {
			return fmt.Errorf("%s: %s", full, msg)
		}
	}
	return fmt.Errorf("%s: %w", full, err)
}

// Output runs an unprivileged command and returns its stdout.
func (r *Runner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := r.exec.Output(ctx, name, args...)
	if err != nil {
		return out, commandError(name, args, err)
	}
	return out, nil
}

// Run runs an unprivileged command with inherited stdio.
func (r *Runner) Run(ctx context.Context, name string, args ...string) error {
	if err := r.exec.Run(ctx, name, args...); err != nil {
		return commandError(name, args, err)
	}
	return nil
}

// OutputPrivileged runs a command as root (directly or via sudo -n).
func (r *Runner) OutputPrivileged(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !r.CanPrivilege() {
		return nil, &PrivilegeError{Err: errors.New("not root and sudo -n is unavailable")}
	}
	n, a := r.privileged(name, args)
	out, err := r.exec.Output(ctx, n, a...)
	if err != nil {
		return out, commandError(n, a, err)
	}
	return out, nil
}

// RunPrivileged runs a command as root with inherited stdio.
func (r *Runner) RunPrivileged(ctx context.Context, name string, args ...string) error {
	if !r.CanPrivilege() {
		return &PrivilegeError{Err: errors.New("not root and sudo -n is unavailable")}
	}
	n, a := r.privileged(name, args)
	if err := r.exec.Run(ctx, n, a...); err != nil {
		return commandError(n, a, err)
	}
	return nil
}

// RunPrivilegedEnv is RunPrivileged with extra environment variables. The
// variables are passed through `env` because sudo otherwise resets the
// environment (notably DEBIAN_FRONTEND).
func (r *Runner) RunPrivilegedEnv(ctx context.Context, env []string, name string, args ...string) error {
	if len(env) == 0 {
		return r.RunPrivileged(ctx, name, args...)
	}
	envArgs := append([]string{}, env...)
	envArgs = append(envArgs, name)
	envArgs = append(envArgs, args...)
	return r.RunPrivileged(ctx, "env", envArgs...)
}

// RunInputPrivileged feeds stdin to a privileged command.
func (r *Runner) RunInputPrivileged(ctx context.Context, input []byte, name string, args ...string) error {
	if !r.CanPrivilege() {
		return &PrivilegeError{Err: errors.New("not root and sudo -n is unavailable")}
	}
	n, a := r.privileged(name, args)
	if err := r.exec.RunInput(ctx, input, n, a...); err != nil {
		return commandError(n, a, err)
	}
	return nil
}

// ReadFile reads a file, falling back to a privileged `cat`.
func (r *Runner) ReadFile(ctx context.Context, path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if r.CanPrivilege() {
		out, catErr := r.OutputPrivileged(ctx, "cat", path)
		if catErr == nil {
			return out, nil
		}
	}
	return nil, err
}

// FileExists reports whether a path exists, using a privileged test(1) when
// the caller cannot stat it directly.
func (r *Runner) FileExists(ctx context.Context, path string) bool {
	if _, err := os.Stat(path); err == nil {
		return true
	}
	if r.CanPrivilege() {
		return r.RunPrivileged(ctx, "test", "-e", path) == nil
	}
	return false
}

// BackupFile creates <path>.pgcli.bak once, preserving permissions.
func (r *Runner) BackupFile(ctx context.Context, path string) error {
	backup := path + ".pgcli.bak"
	if r.FileExists(ctx, backup) {
		return nil
	}
	if r.root {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fi, err := os.Stat(path)
		if err != nil {
			return err
		}
		return os.WriteFile(backup, data, fi.Mode().Perm())
	}
	return r.RunPrivileged(ctx, "cp", "-p", path, backup)
}

// WriteFileAtomic replaces a file via a temporary file and rename. Root
// writes directly; a non-root caller with sudo -n uses tee + chmod + mv.
func (r *Runner) WriteFileAtomic(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	if r.root {
		dir := filepath.Dir(path)
		f, err := os.CreateTemp(dir, ".pgcli.tmp-*")
		if err != nil {
			return err
		}
		tmp := f.Name()
		cleanup := func() {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
		if _, err := f.Write(data); err != nil {
			cleanup()
			return err
		}
		if err := f.Chmod(mode.Perm()); err != nil {
			cleanup()
			return err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return nil
	}
	if !r.CanPrivilege() {
		return &PrivilegeError{Err: errors.New("not root and sudo -n is unavailable")}
	}
	tmp := path + ".pgcli.tmp"
	_ = r.RunPrivileged(ctx, "rm", "-f", tmp)
	if err := r.RunInputPrivileged(ctx, data, "tee", tmp); err != nil {
		return err
	}
	if err := r.RunPrivileged(ctx, "chmod", fmt.Sprintf("%04o", mode.Perm()), tmp); err != nil {
		return err
	}
	return r.RunPrivileged(ctx, "mv", "-f", tmp, path)
}

// LookPath finds a binary even when the caller's PATH omits sbin directories
// (common for non-root shells).
func LookPath(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin"} {
		path := filepath.Join(dir, name)
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s not found (run \"pg zfs install\" first)", name)
}

// EnsureZFSBinaries verifies that both userland tools are installed.
func EnsureZFSBinaries() error {
	if _, err := LookPath("zpool"); err != nil {
		return fmt.Errorf("ZFS is not installed: run \"pg zfs install\" first")
	}
	if _, err := LookPath("zfs"); err != nil {
		return fmt.Errorf("ZFS is not installed: run \"pg zfs install\" first")
	}
	return nil
}

// KernelRelease returns the running kernel release.
func (r *Runner) KernelRelease(ctx context.Context) (string, error) {
	out, err := r.Output(ctx, "uname", "-r")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ModuleVersion returns the loaded ZFS kernel module version and whether it is
// readable.
func (r *Runner) ModuleVersion(ctx context.Context) (string, bool) {
	const path = "/sys/module/zfs/version"
	data, err := os.ReadFile(path)
	if err == nil {
		return strings.TrimSpace(string(data)), true
	}
	if r.CanPrivilege() {
		if out, catErr := r.OutputPrivileged(ctx, "cat", path); catErr == nil {
			return strings.TrimSpace(string(out)), true
		}
	}
	return "", false
}

// LoadedModule reports whether /sys/module/zfs exists.
func (r *Runner) LoadedModule() bool {
	_, err := os.Stat("/sys/module/zfs")
	return err == nil
}

// UserlandVersion returns the userland build, preferring `zpool version`.
func (r *Runner) UserlandVersion(ctx context.Context) (string, error) {
	if zpool, err := LookPath("zpool"); err == nil {
		if line, ok := r.versionLine(ctx, zpool, "version"); ok {
			return line, nil
		}
	}
	// zpool version requires the kernel module; zfs --version still reports
	// the installed userland build when the module is not loaded.
	if zfs, err := LookPath("zfs"); err == nil {
		if line, ok := r.versionLine(ctx, zfs, "--version"); ok {
			return line, nil
		}
	}
	return "", errors.New("cannot determine ZFS userland version")
}

func (r *Runner) versionLine(ctx context.Context, bin string, args ...string) (string, bool) {
	out, _ := r.Output(ctx, bin, args...)
	line := firstLine(out)
	if line == "" && r.CanPrivilege() {
		out, _ = r.OutputPrivileged(ctx, bin, args...)
		line = firstLine(out)
	}
	return line, line != ""
}

func firstLine(out []byte) string {
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

// SecureBootEnabled reports whether the EFI SecureBoot efivar is set.
func SecureBootEnabled() bool {
	matches, _ := filepath.Glob("/sys/firmware/efi/efivars/SecureBoot-*")
	if len(matches) == 0 {
		return false
	}
	data, err := os.ReadFile(matches[0])
	if err != nil || len(data) == 0 {
		return false
	}
	return data[len(data)-1] == 1
}

// ShellJoin renders argv for error messages. It is display-only; commands are
// always executed with an argv array, never through a shell.
func ShellJoin(args []string) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\n'\"$`\\!*?[]{}()&|;<>") {
			parts[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		} else {
			parts[i] = arg
		}
	}
	return strings.Join(parts, " ")
}
