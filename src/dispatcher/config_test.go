// HYDRA-UMC-JOB-DISPATCHER - tests of the configuration file
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package dispatcher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfig_EmptyOrMissingSectionsSwitchEverythingOff(t *testing.T) {
	for _, text := range []string{"", "  \n", "{}", `{"replan":null}`} {
		c, err := ParseConfig([]byte(text))
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if c.LocationSetting().Enabled || c.ReplanSetting().Enabled || c.IntervalSetting().Enabled {
			t.Fatalf("%q switched something on", text)
		}
	}
}

func TestConfig_ASectionThatIsNotEnabledStaysOff(t *testing.T) {
	c, err := ParseConfig([]byte(`{"location_costs":{"costs":{"A":{"B":1}}},"replan":{"on_stale":true},"eta_interval":{"min_samples":4}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.LocationSetting().Enabled || c.ReplanSetting().Enabled || c.IntervalSetting().Enabled {
		t.Fatal("a section without enabled:true must stay off")
	}
}

func TestConfig_FullFile(t *testing.T) {
	c, err := ParseConfig([]byte(`{
		"location_costs": {"enabled": true, "costs": {"A": {"B": 2.5}}},
		"replan": {"enabled": true, "on_fault": false},
		"eta_interval": {"enabled": true, "low_percentile": 10, "high_percentile": 90, "min_samples": 5}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if l := c.LocationSetting(); !l.Enabled || l.Costs["A"]["B"] != 2.5 {
		t.Fatalf("location = %+v", l)
	}
	if r := c.ReplanSetting(); !r.Enabled || !r.OnStale || r.OnFault {
		t.Fatalf("replan = %+v, want on_stale defaulting to true and on_fault false", r)
	}
	if iv := c.IntervalSetting(); !iv.Enabled || iv.LowPercentile != 10 || iv.HighPercentile != 90 || iv.MinSamples != 5 {
		t.Fatalf("interval = %+v", iv)
	}
}

func TestConfig_Refusals(t *testing.T) {
	for name, text := range map[string]string{
		"unknown field":     `{"replann":{"enabled":true}}`,
		"unknown inner":     `{"replan":{"enabled":true,"on_stal":true}}`,
		"negative cost":     `{"location_costs":{"enabled":true,"costs":{"A":{"B":-1}}}}`,
		"percentiles":       `{"eta_interval":{"enabled":true,"low_percentile":80,"high_percentile":20}}`,
		"over 100":          `{"eta_interval":{"enabled":true,"low_percentile":10,"high_percentile":101}}`,
		"negative min":      `{"eta_interval":{"enabled":true,"min_samples":-1}}`,
		"not json":          `enabled`,
		"trailing document": `{} {}`,
		"wrong type":        `{"replan":{"enabled":"yes"}}`,
	} {
		if _, err := ParseConfig([]byte(text)); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestConfig_LoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dispatcher.json")
	if err := os.WriteFile(path, []byte(`{"replan":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfigFile(path)
	if err != nil || !c.ReplanSetting().Enabled {
		t.Fatalf("c = %+v err = %v", c, err)
	}
	if _, err := LoadConfigFile(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("a file that does not exist must be refused")
	}
}
