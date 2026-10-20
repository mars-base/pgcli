package config

import (
	"path/filepath"
	"testing"
)

func TestTuneSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pg.yaml")

	cfg := Default()
	cfg.Instances = map[string]InstanceConfig{
		"a": {Tune: &TuneConfig{
			Inputs: map[string]string{"type": "oltp", "ram": "16GB", "hd": "nvme"},
			Params: map[string]string{"shared_buffers": "4GB", "work_mem": "19MB", "jit": "off"},
		}},
		"plain": {},
	}
	cfg.Addons.Patroni = map[string]PatroniClusterConfig{
		"app": {Name: "app", Tune: &TuneConfig{
			Inputs: map[string]string{"type": "dw"},
			Params: map[string]string{"max_worker_processes": "8"},
		}},
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	tune := got.Instances["a"].Tune
	if tune == nil {
		t.Fatal("instance tune not persisted")
	}
	if tune.Inputs["type"] != "oltp" || tune.Inputs["ram"] != "16GB" {
		t.Errorf("tune inputs not persisted: %v", tune.Inputs)
	}
	if tune.Params["shared_buffers"] != "4GB" || tune.Params["jit"] != "off" {
		t.Errorf("tune params not persisted: %v", tune.Params)
	}
	if got.Instances["plain"].Tune != nil {
		t.Error("untuned instance must not gain a Tune pointer (omitempty)")
	}

	ct := got.Addons.Patroni["app"].Tune
	if ct == nil || ct.Params["max_worker_processes"] != "8" {
		t.Errorf("cluster tune not persisted: %+v", ct)
	}
}
