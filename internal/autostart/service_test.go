package autostart

import (
	"strings"
	"testing"
)

func TestForResolvesAbsolutePaths(t *testing.T) {
	svc, err := For("relative-config.yaml")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if !strings.HasPrefix(svc.ConfigPath, "/") {
		t.Errorf("ConfigPath not absolute: %s", svc.ConfigPath)
	}
	if !strings.HasPrefix(svc.Binary, "/") {
		t.Errorf("Binary not absolute: %s", svc.Binary)
	}
	if !strings.HasPrefix(svc.UnitName, "pgcli-autostart-") {
		t.Errorf("unexpected UnitName: %s", svc.UnitName)
	}
}

func TestForDistinctConfigsDistinctUnits(t *testing.T) {
	a, _ := For("/tmp/a.yaml")
	b, _ := For("/tmp/b.yaml")
	if a.UnitName == b.UnitName {
		t.Errorf("unit names collide: %s", a.UnitName)
	}
}
