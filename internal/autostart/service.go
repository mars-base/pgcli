// Package autostart manages the per-user boot service that runs
// `pg start --autostart` after a host reboot: a systemd user unit on Linux,
// a launchd LaunchAgent on macOS.
package autostart

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Service is a boot service definition for one config file.
type Service struct {
	// UnitName is the systemd unit name / launchd label.
	UnitName string
	// Binary is the absolute path to the pg binary.
	Binary string
	// ConfigPath is the absolute path to the config file.
	ConfigPath string
}

// For builds the Service definition for the given config file path.
func For(cfgPath string) (*Service, error) {
	absCfg, err := filepath.Abs(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("resolving config path: %w", err)
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolving pg binary path: %w", err)
	}
	absBinary, err := filepath.Abs(binary)
	if err != nil {
		return nil, fmt.Errorf("resolving pg binary path: %w", err)
	}
	sum := sha256.Sum256([]byte(absCfg))
	slug := hex.EncodeToString(sum[:4])
	return &Service{
		UnitName:   "pgcli-autostart-" + slug,
		Binary:     absBinary,
		ConfigPath: absCfg,
	}, nil
}

// Info describes the current boot service state for status output.
type Info struct {
	UnitName   string
	ConfigPath string
	Installed  bool
	Enabled    bool
	Running    bool
	Linger     string // linger state (Linux only; empty when unknown)
	Stale      string // non-empty when the unit points at a different binary
}

// Installer abstracts platform-specific boot service operations.
type Installer interface {
	// Install writes the service definition and enables it.
	Install(svc *Service) error
	// Uninstall disables and removes the service definition.
	Uninstall(svc *Service) error
	// Status reports the current state of the service.
	Status(svc *Service) (*Info, error)
}
