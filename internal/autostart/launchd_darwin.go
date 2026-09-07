//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func plistPath(label string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

func renderPlist(svc *Service) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>-c</string>
		<string>%s</string>
		<string>start</string>
		<string>--autostart</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>StandardOutPath</key>
	<string>/tmp/%s.log</string>
	<key>StandardErrorPath</key>
	<string>/tmp/%s.log</string>
</dict>
</plist>
`, svc.UnitName, svc.Binary, svc.ConfigPath, svc.UnitName, svc.UnitName)
}

type launchdInstaller struct{}

// NewInstaller returns the platform boot service installer.
func NewInstaller() Installer { return launchdInstaller{} }

func (launchdInstaller) Install(svc *Service) error {
	path, err := plistPath(svc.UnitName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating LaunchAgents dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(renderPlist(svc)), 0644); err != nil {
		return fmt.Errorf("writing plist: %w", err)
	}

	target := fmt.Sprintf("gui/%d", os.Getuid())
	out, err := exec.Command("launchctl", "bootstrap", target, path).CombinedOutput()
	if err != nil {
		// Fall back to legacy load.
		if out2, err2 := exec.Command("launchctl", "load", "-w", path).CombinedOutput(); err2 != nil {
			return fmt.Errorf("launchctl bootstrap: %s; launchctl load: %s", strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
		}
	}
	fmt.Println("  [i] LaunchAgent runs at GUI login (rootless podman cannot start earlier)")
	return nil
}

func (launchdInstaller) Uninstall(svc *Service) error {
	path, err := plistPath(svc.UnitName)
	if err != nil {
		return err
	}
	target := fmt.Sprintf("gui/%d", os.Getuid())
	exec.Command("launchctl", "bootout", target+"/"+svc.UnitName).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing plist: %w", err)
	}
	return nil
}

func (launchdInstaller) Status(svc *Service) (*Info, error) {
	info := &Info{UnitName: svc.UnitName, ConfigPath: svc.ConfigPath}

	path, err := plistPath(svc.UnitName)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		info.Installed = true
	}

	if info.Installed {
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), svc.UnitName)
		if out, err := exec.Command("launchctl", "print", target).Output(); err == nil {
			info.Enabled = true
			if strings.Contains(string(out), "state = running") {
				info.Running = true
			}
		}
		data, err := os.ReadFile(path)
		if err == nil && !strings.Contains(string(data), "<string>"+svc.Binary+"</string>") {
			info.Stale = "agent points at a different pg binary -- run 'pg autostart enable' again"
		}
	}
	return info, nil
}
