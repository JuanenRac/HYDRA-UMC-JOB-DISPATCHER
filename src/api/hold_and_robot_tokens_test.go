package api

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/sqlitestore"
)

func heldEngine(t *testing.T) *dispatcher.Engine {
	t.Helper()
	engine := dispatcher.NewEngine()
	if err := engine.SetHoldEnabled(true); err != nil {
		t.Fatal(err)
	}
	available := true
	engine.UpdateRobot(dispatcher.RobotUpdate{ID: "arm-1", Available: &available})
	if err := engine.AddJob(dispatcher.Job{ID: "job-1"}); err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestHold_OffTheRoutesDoNotExist(t *testing.T) {
	s := New(dispatcher.NewEngine())
	for _, path := range []string{"/dispatch/hold", "/dispatch/release"} {
		if rec := withToken(t, s, http.MethodPost, path, "", nil); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s with the hold off = %d, want 404", path, rec.Code)
		}
	}
}

func TestHold_NothingIsAssignedWhileItIsHeld(t *testing.T) {
	engine := heldEngine(t)
	s := New(engine)
	if rec := withToken(t, s, http.MethodPost, "/dispatch/hold", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("hold = %d", rec.Code)
	}
	if rec := withToken(t, s, http.MethodPost, "/dispatch", "", nil); rec.Code != http.StatusLocked {
		t.Errorf("POST /dispatch while held = %d, want 423", rec.Code)
	}
	for pass := 0; pass < 3; pass++ {
		if got := engine.DispatchOnce(); len(got) != 0 {
			t.Fatalf("a held engine assigned %v", got)
		}
	}
	if job, _ := engine.Job("job-1"); job.Status != dispatcher.StatusPending {
		t.Errorf("the queued job became %q while held", job.Status)
	}
	if rec := withToken(t, s, http.MethodPost, "/dispatch/release", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("release = %d", rec.Code)
	}
	if got := engine.DispatchOnce(); len(got) != 1 {
		t.Errorf("after the release the pass assigned %v, want one job", got)
	}
}

func TestHold_OnlyTheTokenOfTheAPIOrAnAdminReleases(t *testing.T) {
	engine := heldEngine(t)
	s := New(engine)
	s.RequireToken("s3cret-token")
	s.SetIdentityKey(identityTestKey)
	if rec := withToken(t, s, http.MethodPost, "/dispatch/hold", signedFor("operator"), nil); rec.Code != http.StatusOK {
		t.Fatalf("an operator holds, got %d", rec.Code)
	}
	if rec := withToken(t, s, http.MethodPost, "/dispatch/release", signedFor("operator"), nil); rec.Code != http.StatusForbidden {
		t.Errorf("an operator releasing = %d, want 403", rec.Code)
	}
	if !engine.Held() {
		t.Fatal("a refused release released")
	}
	if rec := withToken(t, s, http.MethodPost, "/dispatch/release", signedFor("admin"), nil); rec.Code != http.StatusOK {
		t.Errorf("an admin releasing = %d, want 200", rec.Code)
	}
}

func TestHold_SurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	open := func() (*dispatcher.Engine, *sqlitestore.Store) {
		store, err := sqlitestore.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		engine, err := dispatcher.NewEngineWithStore(store)
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.SetHoldEnabled(true); err != nil {
			t.Fatal(err)
		}
		return engine, store
	}
	engine, store := open()
	if err := engine.SetHeld(true); err != nil {
		t.Fatal(err)
	}
	store.Close()
	engine, store = open()
	defer store.Close()
	if !engine.Held() {
		t.Error("the hold was lost with the restart")
	}
}

func TestRobotTokens_ARobotSpeaksOnlyForItself(t *testing.T) {
	engine := dispatcher.NewEngine()
	s := New(engine)
	s.RequireToken("s3cret-token")
	env := map[string]string{"A": "token-of-arm-1", "B": "token-of-arm-2"}
	tokens, err := RobotTokens(map[string]string{"arm-1": "A", "arm-2": "B"}, "s3cret-token", func(n string) string { return env[n] })
	if err != nil {
		t.Fatal(err)
	}
	s.SetRobotTokens(tokens)
	beat := func(token, robot string) int {
		return withToken(t, s, http.MethodPost, "/robots", token, []byte(`{"id":"`+robot+`"}`)).Code
	}
	if got := beat("token-of-arm-1", "arm-1"); got != http.StatusOK {
		t.Errorf("a robot with its own token = %d, want 200", got)
	}
	if got := beat("token-of-arm-2", "arm-1"); got != http.StatusForbidden {
		t.Errorf("another robot in its name = %d, want 403", got)
	}
	if got := beat("token-of-arm-1", "arm-3"); got != http.StatusForbidden {
		t.Errorf("a robot token for a robot without one = %d, want 403", got)
	}
	if got := beat("s3cret-token", "arm-1"); got != http.StatusOK {
		t.Errorf("the token of the API = %d, want 200", got)
	}
	if got := beat("s3cret-token", "arm-3"); got != http.StatusOK {
		t.Errorf("a robot without a token of its own, with the token of the API = %d, want 200", got)
	}
	// a robot token opens nothing else
	for _, path := range []string{"/jobs", "/dispatch"} {
		if rec := withToken(t, s, http.MethodPost, path, "token-of-arm-1", []byte("{}")); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s with a robot token = %d, want 401", path, rec.Code)
		}
	}
	if err := engine.AddJob(dispatcher.Job{ID: "job-1"}); err != nil {
		t.Fatal(err)
	}
	available := true
	engine.UpdateRobot(dispatcher.RobotUpdate{ID: "arm-1", Available: &available})
	if assigned := engine.DispatchOnce(); len(assigned) != 1 || assigned[0].RobotID != "arm-1" {
		t.Fatalf("assigned %v, want job-1 to arm-1", assigned)
	}
	own, other := "token-of-arm-1", "token-of-arm-2"
	body := []byte(`{"id":"job-1","robotId":"arm-1","success":true}`)
	if rec := withToken(t, s, http.MethodPost, "/jobs/complete", other, body); rec.Code != http.StatusForbidden {
		t.Errorf("closing the job of another robot = %d, want 403", rec.Code)
	}
	if rec := withToken(t, s, http.MethodPost, "/jobs/complete", own, body); rec.Code != http.StatusOK {
		t.Errorf("closing its own job = %d, want 200", rec.Code)
	}
}

func TestRobotTokens_ABadSectionIsRefused(t *testing.T) {
	env := map[string]string{"A": "same", "B": "same", "API": "s3cret-token"}
	getenv := func(n string) string { return env[n] }
	for name, section := range map[string]map[string]string{
		"empty variable":   {"arm-1": "MISSING"},
		"shared token":     {"arm-1": "A", "arm-2": "B"},
		"token of the API": {"arm-1": "API"},
		"no variable name": {"arm-1": ""},
	} {
		if _, err := RobotTokens(section, "s3cret-token", getenv); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
