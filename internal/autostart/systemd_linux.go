//go:build linux

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// unitDir returns the systemd user unit directory.
func unitDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "systemd", "user"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func unitPath(unitName string) (string, error) {
	dir, err := unitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, unitName+".service"), nil
}

func renderUnit(svc *Service) string {
	return fmt.Sprintf(`[Unit]
Description=pgcli autostart (start pg instances with autostart enabled)
ConditionPathExists=%s

[Service]
Type=oneshot
Environment=PATH=%%h/.local/bin:/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin
ExecStart=%s -c %s start --autostart
TimeoutStartSec=1800

[Install]
WantedBy=default.target
`, svc.Binary, svc.Binary, svc.ConfigPath)
}

type systemdInstaller struct{}

// NewInstaller returns the platform boot service installer.
func NewInstaller() Installer { return systemdInstaller{} }

func (systemdInstaller) Install(svc *Service) error {
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		return fmt.Errorf("XDG_RUNTIME_DIR is not set -- systemd user units require a user session (are you in an SSH session without systemd --user?)")
	}
	path, err := unitPath(svc.UnitName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating unit dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(renderUnit(svc)), 0644); err != nil {
		return fmt.Errorf("writing unit file: %w", err)
	}

	steps := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", svc.UnitName},
	}
	for _, s := range steps {
		cmd := exec.Command(s[0], s[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %s", strings.Join(s, " "), strings.TrimSpace(string(out)))
		}
	}

	if err := enableLinger(); err != nil {
		fmt.Printf("  [!] loginctl enable-linger failed: %v\n", err)
		fmt.Println("      The service will start at user login instead of at boot.")
		fmt.Println("      Run 'loginctl enable-linger <user>' to enable true boot-time start.")
	}
	return nil
}

func (systemdInstaller) Uninstall(svc *Service) error {
	exec.Command("systemctl", "--user", "disable", "--now", svc.UnitName).Run()
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	path, err := unitPath(svc.UnitName)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing unit file: %w", err)
	}
	return nil
}

func (systemdInstaller) Status(svc *Service) (*Info, error) {
	info := &Info{UnitName: svc.UnitName, ConfigPath: svc.ConfigPath}

	path, err := unitPath(svc.UnitName)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		info.Installed = true
	}

	if info.Installed {
		if out, err := exec.Command("systemctl", "--user", "is-enabled", svc.UnitName).Output(); err == nil {
			info.Enabled = strings.TrimSpace(string(out)) == "enabled"
		}
		if err := exec.Command("systemctl", "--user", "is-active", "--quiet", svc.UnitName).Run(); err == nil {
			info.Running = true
		}
		data, err := os.ReadFile(path)
		if err == nil && !strings.Contains(string(data), "ExecStart="+svc.Binary+" ") {
			info.Stale = "unit points at a different pg binary -- run 'pg autostart enable' again"
		}
	}

	if u, err := user.Current(); err == nil {
		if out, err := exec.Command("loginctl", "show-user", u.Username, "-p", "Linger").Output(); err == nil {
			info.Linger = strings.TrimSpace(strings.TrimPrefix(string(out), "Linger="))
		}
	}
	return info, nil
}

func enableLinger() error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	out, err := exec.Command("loginctl", "enable-linger", u.Username).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}
