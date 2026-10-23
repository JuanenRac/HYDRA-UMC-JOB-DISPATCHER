// HYDRA-UMC-JOB-DISPATCHER - src/dispatcher/tools_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The tool catalog only warns: an unknown tool is logged once and the job or robot is accepted all the same.
package dispatcher

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func catalogWithLog(names ...string) (*ToolCatalog, *[]string) {
	c := NewToolCatalog(names)
	lines := []string{}
	c.logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	return c, &lines
}

func TestToolCatalog_KnownEmptyAndNilNeverWarn(t *testing.T) {
	c, lines := catalogWithLog("PnP", "Laser")
	for _, tool := range []string{"PnP", "Laser", ""} {
		if !c.Check(tool, "x") {
			t.Errorf("%q should be known", tool)
		}
	}
	var none *ToolCatalog
	if !none.Check("Anything", "x") || !none.Knows("Anything") {
		t.Error("a nil catalog knows every tool")
	}
	if len(*lines) != 0 {
		t.Errorf("no warning expected, got %v", *lines)
	}
}

func TestToolCatalog_AnUnknownToolIsWarnedAboutOnce(t *testing.T) {
	c, lines := catalogWithLog("PnP")
	if c.Check("Drill", `job "a"`) {
		t.Fatal("Drill is not in the catalog")
	}
	c.Check("Drill", `job "b"`)
	if len(*lines) != 1 || !strings.Contains((*lines)[0], `"Drill"`) || !strings.Contains((*lines)[0], `job "a"`) {
		t.Fatalf("expected one warning naming the tool and its origin, got %v", *lines)
	}
}

func TestToolCatalog_ACaseDifferenceGetsAHint(t *testing.T) {
	c, lines := catalogWithLog("PnP")
	c.Check("pnp", `robot "r1"`)
	if len(*lines) != 1 || !strings.Contains((*lines)[0], `did you mean "PnP"`) {
		t.Fatalf("expected a hint, got %v", *lines)
	}
}

func TestEngine_AnUnknownToolIsWarnedAboutButAccepted(t *testing.T) {
	e := NewEngine()
	c, lines := catalogWithLog("PnP")
	e.SetToolCatalog(c)

	e.UpsertRobot(Robot{ID: "r1", Available: true, Tool: "Welder"})
	if err := e.AddJob(Job{ID: "j1", RequiredTool: "Drill"}); err != nil {
		t.Fatalf("an unknown tool must not refuse the job: %v", err)
	}
	if _, ok := e.Job("j1"); !ok {
		t.Fatal("the job was not stored")
	}
	got := strings.Join(*lines, "\n")
	if !strings.Contains(got, `Tool of robot "r1"`) || !strings.Contains(got, `RequiredTool of job "j1"`) {
		t.Fatalf("expected a warning for the robot and for the job, got:\n%s", got)
	}
}

func TestEngine_ARetriedJobIsCheckedToo(t *testing.T) {
	e := NewEngine()
	c, lines := catalogWithLog("PnP")
	e.SetToolCatalog(c)
	if _, _, err := e.SubmitJob(Job{ID: "j1", DedupKey: "k", RequiredTool: "PnP"}); err != nil {
		t.Fatal(err)
	}
	if err := e.CancelJob("j1"); err != nil {
		t.Fatal(err)
	}
	if _, result, err := e.SubmitJob(Job{ID: "other", DedupKey: "k", RequiredTool: "Typo"}); err != nil || result != SubmitRetried {
		t.Fatalf("retry result %v, err %v", result, err)
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], `"Typo"`) {
		t.Fatalf("expected a warning for the retry, got %v", *lines)
	}
}

func TestEngine_WithoutACatalogNothingChanges(t *testing.T) {
	e := NewEngine()
	e.UpsertRobot(Robot{ID: "r1", Available: true, Tool: "Anything"})
	if err := e.AddJob(Job{ID: "j1", RequiredTool: "Anything"}); err != nil {
		t.Fatal(err)
	}
}

func TestParseToolNames_AcceptsNamesProfilesAndAnObject(t *testing.T) {
	cases := map[string][]string{
		`["PnP","Laser"]`: {"PnP", "Laser"},
		`[{"name":"TOOL_PNP"},{"id":"TOOL_LASER"},{"profile":"TOOL_DRILL"},{"tool":"T4"}]`: {"TOOL_PNP", "TOOL_LASER", "TOOL_DRILL", "T4"},
		`{"tools":["A",{"name":"B"}]}`: {"A", "B"},
	}
	for input, want := range cases {
		got, err := ParseToolNames([]byte(input))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, %v want %v", input, got, err, want)
		}
	}
}

func TestParseToolNames_RejectsWhatItCannotRead(t *testing.T) {
	for _, input := range []string{`not json`, `{"x":1}`, `"PnP"`, `[1]`, `[{"colour":"red"}]`} {
		if _, err := ParseToolNames([]byte(input)); err == nil {
			t.Errorf("%s should be rejected", input)
		}
	}
}

func TestLoadToolCatalogFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(path, []byte(`["PnP", " Laser ", ""]`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadToolCatalogFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Names(), []string{"Laser", "PnP"}) {
		t.Fatalf("names: %v", c.Names())
	}
	if _, err := LoadToolCatalogFile(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("a missing file must be an error")
	}
}
