package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/api"
	"github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER/src/dispatcher"
)

func writeConfig(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func replanOf(e *dispatcher.Engine) bool { return e.ReplanEnabled() }

func TestReload_InstallsTheNewValuesAndAFileThatIsNotValidChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dispatcher.json")
	writeConfig(t, path, `{"replan":{"enabled":true,"allow_unconfirmed":false,"stop_ack_timeout_seconds":30}}`)
	engine := dispatcher.NewEngine()
	reloader := newConfigReloader(path, engine, false, time.Minute, dispatcher.DefaultETAWindow)

	summary, err := reloader.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if !replanOf(engine) {
		t.Fatal("replanning should be on after the reload")
	}
	replan := summary["replan"].(map[string]any)
	if replan["allowUnconfirmed"] != false || replan["stopAckTimeoutSeconds"] != 30.0 {
		t.Fatalf("summary %v", replan)
	}

	writeConfig(t, path, `{"replan":{"enabled":true,"allow_unconfirmed":true,"stop_ack_timeout_seconds":120}}`)
	summary, err = reloader.Reload()
	if err != nil || summary["replan"].(map[string]any)["stopAckTimeoutSeconds"] != 120.0 {
		t.Fatalf("second reload: %v %v", summary, err)
	}

	for name, bad := range map[string]string{
		"not json":           `{`,
		"unknown key":        `{"replan":{"enabled":true,"allow_unconfirm":true}}`,
		"timeout too large":  `{"replan":{"enabled":true,"stop_ack_timeout_seconds":100000}}`,
		"timeout is zero":    `{"replan":{"enabled":true,"stop_ack_timeout_seconds":0}}`,
		"after the json":     `{} {}`,
		"a range needs -eta": `{"eta_interval":{"enabled":true}}`,
	} {
		writeConfig(t, path, bad)
		if _, err := reloader.Reload(); err == nil {
			t.Errorf("%s: a file that is not valid was accepted", name)
		}
		// the values of the second reload are still in force: switching replanning off would show here
		if !replanOf(engine) {
			t.Fatalf("%s: the previous values did not stay in force", name)
		}
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := reloader.Reload(); err == nil || !replanOf(engine) {
		t.Fatalf("a file that is gone must change nothing: %v", err)
	}
}

func TestReload_AValidEmptyFileSwitchesTheOptionalBehavioursOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dispatcher.json")
	writeConfig(t, path, `{"replan":{"enabled":true}}`)
	engine := dispatcher.NewEngine()
	reloader := newConfigReloader(path, engine, false, time.Minute, dispatcher.DefaultETAWindow)
	if _, err := reloader.Reload(); err != nil || !replanOf(engine) {
		t.Fatalf("on: %v", err)
	}
	writeConfig(t, path, `{}`)
	if _, err := reloader.Reload(); err != nil || replanOf(engine) {
		t.Fatalf("an empty file is valid and means every switch off: %v", err)
	}
}

func TestReload_TheRangesOfTheEstimatesAreReloadedOnlyWithTheEstimatesOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dispatcher.json")
	writeConfig(t, path, `{"eta_interval":{"enabled":true}}`)
	engine := dispatcher.NewEngine()
	if _, err := newConfigReloader(path, engine, false, time.Minute, 10).Reload(); err == nil {
		t.Fatal("a range without -eta must be refused")
	}
	engine.SetETA(dispatcher.ETAConfig{Enabled: true, DefaultDuration: time.Minute, Window: 10})
	summary, err := newConfigReloader(path, engine, true, time.Minute, 10).Reload()
	if err != nil || summary["etaInterval"] != true {
		t.Fatalf("%v %v", summary, err)
	}
}

func reloadRequest(server *api.Server, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/reload-config", strings.NewReader(""))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func TestReloadRoute_IsUnknownWithoutAFile_LocalOnly_AndAnswers422ForAFileThatIsNotValid(t *testing.T) {
	engine := dispatcher.NewEngine()
	server := api.New(engine)
	if rec := reloadRequest(server, "127.0.0.1:5000"); rec.Code != http.StatusNotFound {
		t.Fatalf("without -config the route must not exist: %d", rec.Code)
	}

	path := filepath.Join(t.TempDir(), "dispatcher.json")
	writeConfig(t, path, `{"replan":{"enabled":true}}`)
	server.SetConfigReloader(newConfigReloader(path, engine, false, time.Minute, 10).Reload)

	if rec := reloadRequest(server, "192.168.0.50:5000"); rec.Code != http.StatusForbidden {
		t.Fatalf("a caller from the network must be refused: %d", rec.Code)
	}
	if replanOf(engine) {
		t.Fatal("a refused caller must not have changed anything")
	}
	if rec := reloadRequest(server, "127.0.0.1:5000"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reloaded":true`) || !replanOf(engine) {
		t.Fatalf("local reload: %d %s", rec.Code, rec.Body.String())
	}
	if rec := reloadRequest(server, "[::1]:5000"); rec.Code != http.StatusOK {
		t.Fatalf("the IPv6 loopback is local too: %d", rec.Code)
	}

	writeConfig(t, path, `{"replan":`)
	rec := reloadRequest(server, "127.0.0.1:5000")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "previous values") || !replanOf(engine) {
		t.Fatalf("invalid file: %d %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/reload-config", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, req)
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("only POST: %d", getRec.Code)
	}
}

func TestReloadRoute_NeedsTheTokenWhenOneIsSet(t *testing.T) {
	engine := dispatcher.NewEngine()
	server := api.New(engine)
	server.RequireToken("secret")
	path := filepath.Join(t.TempDir(), "dispatcher.json")
	writeConfig(t, path, `{"replan":{"enabled":true}}`)
	server.SetConfigReloader(newConfigReloader(path, engine, false, time.Minute, 10).Reload)
	if rec := reloadRequest(server, "127.0.0.1:5000"); rec.Code != http.StatusUnauthorized || replanOf(engine) {
		t.Fatalf("no token: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/reload-config", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !replanOf(engine) {
		t.Fatalf("with the token: %d", rec.Code)
	}
}
