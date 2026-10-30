package gui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/testenv"
)

const omoCategoriesURL = "/api/agents/omo/categories"
const omoCategoriesFixture = `{
  // keep untouched
  "categories": {"quick": {"models": ["vendor/first", {"model": "vendor/second", "temperature": 0.2}], "reasoning": "low"}},
  "other": {"keep": true}
}`

func TestOmoCategoriesNamedAgentsHTTP(t *testing.T) {
	// Premise: the real route reads an isolated config with no agent override.
	p := omoCategoriesHome(t)
	if err := os.WriteFile(p, []byte(omoCategoriesFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	h := Handler(nil, nil)
	get := httptest.NewRecorder()
	h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, omoCategoriesURL, nil))
	var doc agent.OmoCategories
	if err := json.Unmarshal(get.Body.Bytes(), &doc); err != nil || get.Code != http.StatusOK || len(doc.Agents) != 4 || doc.Agents[0].Name != "explore" || len(doc.Agents[0].Models) != 0 {
		t.Fatalf("GET agents: %d %s %v", get.Code, get.Body, err)
	}
	// Action: save a category and a named agent through the same document.
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, omoCategoriesURL, strings.NewReader(`{"revision":"`+doc.Revision+`","changes":[{"scope":"categories","name":"quick","reasoning":{"value":"high"}},{"scope":"agents","name":"plan-consultant","models":[{"model":"p/planner"}],"reasoning":{"value":"custom"}}]}`)))
	var after agent.OmoCategories
	if err := json.Unmarshal(post.Body.Bytes(), &after); err != nil || post.Code != http.StatusOK || len(after.Agents) != 4 || after.Agents[2].Models[0].Model != "p/planner" || *after.Agents[2].Reasoning != "custom" || *after.Categories[0].Reasoning != "high" {
		t.Fatalf("POST agents: %d %s %v", post.Code, post.Body, err)
	}
	// Assertion: the following GET and the successful save share one revision.
	reloaded := httptest.NewRecorder()
	h.ServeHTTP(reloaded, httptest.NewRequest(http.MethodGet, omoCategoriesURL, nil))
	var readback agent.OmoCategories
	if err := json.Unmarshal(reloaded.Body.Bytes(), &readback); err != nil || reloaded.Code != http.StatusOK || readback.Revision != after.Revision || readback.Agents[2].Models[0].Model != "p/planner" {
		t.Fatalf("readback: %d %s %v", reloaded.Code, reloaded.Body, err)
	}
}

func omoCategoriesHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	for key, part := range map[string]string{
		"XDG_CONFIG_HOME": ".config", "XDG_CACHE_HOME": ".cache",
		"XDG_DATA_HOME": ".local/share", "XDG_STATE_HOME": ".local/state",
		"APPDATA": "AppData/Roaming", "LOCALAPPDATA": "AppData/Local",
	} {
		t.Setenv(key, filepath.Join(home, part))
	}
	for _, key := range agentenv.Vars {
		t.Setenv(key, "")
	}
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(home, ".omo", "omo.jsonc")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOmoCategoriesRejectsMalformedRequests(t *testing.T) {
	for _, body := range []string{
		`{`, `null`, `[]`, `{"path":"/tmp/elsewhere"}`,
		`{"revision":"r","changes":[],"extra":true}`,
		`{"revision":"r","changes":[{"name":"quick","extra":true}]}`,
		`{"revision":"r","changes":[]} {}`,
		`{"revision":"r","changes":[]} trailing`,
	} {
		t.Run(body, func(t *testing.T) {
			// Premise: the real config lives under an isolated HOME.
			path := omoCategoriesHome(t)
			if err := os.WriteFile(path, []byte(omoCategoriesFixture), 0o600); err != nil {
				t.Fatal(err)
			}
			doc, err := agent.ReadOmoCategories()
			if err != nil {
				t.Fatal(err)
			}
			// Action: post an invalid request through the whole Handler.
			rec := httptest.NewRecorder()
			Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, omoCategoriesURL, strings.NewReader(strings.ReplaceAll(body, `"r"`, `"`+doc.Revision+`"`))))
			// Assertion: a displayable error comes back and the config is not overwritten.
			var out struct{ Error string }
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusBadRequest || out.Error == "" {
				t.Fatalf("invalid input: %d %s (%v)", rec.Code, rec.Body, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != omoCategoriesFixture {
				t.Fatalf("rejected request changed config: %s (%v)", got, err)
			}
		})
	}
}

func TestOmoCategoriesMissingFile(t *testing.T) {
	// Premise: the isolated HOME has no global config.
	omoCategoriesHome(t)
	// Action: read the categories.
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, omoCategoriesURL, nil))
	// Assertion: a missing config is told apart from empty categories.
	var out struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusNotFound || out.Error == "" {
		t.Fatalf("missing file: %d %s (%v)", rec.Code, rec.Body, err)
	}
}

func TestOmoCategoriesUsesHandlerProtection(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			// Premise: the category route sits behind the existing Handler guards.
			omoCategoriesHome(t)
			r := httptest.NewRequest(method, omoCategoriesURL, strings.NewReader(`{}`))
			r.Header.Set("Origin", "null")
			// Action: an untrusted page asks for the config.
			rec := httptest.NewRecorder()
			Handler(nil, nil).ServeHTTP(rec, r)
			// Assertion: it is refused before any read or write.
			if rec.Code != http.StatusForbidden {
				t.Fatalf("untrusted page: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestOmoCategoriesUsesWebAuthentication(t *testing.T) {
	// Premise: the browser side uses the existing authentication wrapper.
	omoCategoriesHome(t)
	h := webGuard("categories", "secret", 0, Handler(nil, nil))
	// Action: a request without the auth cookie.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, omoCategoriesURL, nil))
	// Assertion: an unauthenticated request cannot read a local file.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d %s", rec.Code, rec.Body)
	}
}

func TestOmoCategoriesReadIOError(t *testing.T) {
	// Premise: the config path is a directory, which cannot be read as a file.
	path := omoCategoriesHome(t)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	// Action: read through the real Handler.
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, omoCategoriesURL, nil))
	// Assertion: an I/O error is not passed off as success or as no categories.
	var out struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusInternalServerError || out.Error == "" {
		t.Fatalf("read IO error: %d %s (%v)", rec.Code, rec.Body, err)
	}
}

func TestOmoCategoriesOnlyLocalRoute(t *testing.T) {
	for _, url := range []string{"/api/agents/codex/categories", "/api/agents/omo@wsl:Ubuntu/categories", "/api/agents/omo/categories/other"} {
		t.Run(url, func(t *testing.T) {
			// Premise: only the fixed local category route is served.
			path := omoCategoriesHome(t)
			if err := os.WriteFile(path, []byte(omoCategoriesFixture), 0o600); err != nil {
				t.Fatal(err)
			}
			// Action: post to a route that is not the target.
			rec := httptest.NewRecorder()
			Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{}`)))
			// Assertion: the request fails and no local file is edited.
			if rec.Code < 400 {
				t.Fatalf("non-local route accepted: %d %s", rec.Code, rec.Body)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != omoCategoriesFixture {
				t.Fatalf("non-local request changed config: %s (%v)", got, err)
			}
		})
	}
}

func TestOmoCategoriesLiveSaveAndReload(t *testing.T) {
	// Premise: the real HTTP server and the on-disk config both sit in an isolated environment.
	path := omoCategoriesHome(t)
	if err := os.WriteFile(path, []byte(omoCategoriesFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler(nil, nil))
	defer server.Close()
	call := func(method, body string) agent.OmoCategories {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+omoCategoriesURL, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s %s: %d %s", method, omoCategoriesURL, res.StatusCode, b)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("HTTP: %d %s", res.StatusCode, b)
		}
		var doc agent.OmoCategories
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	before := call(http.MethodGet, "")
	// Action: swap and edit object models, save, then read again.
	saved := call(http.MethodPost, `{"revision":"`+before.Revision+`","changes":[{"name":"quick","models":[{"model":"custom/replaced","index":1},{"model":"vendor/first","index":0}],"reasoning":{"value":"high"}}]}`)
	after := call(http.MethodGet, "")
	// Assertion: the read-back matches the disk, keeping object extras and the regions not edited.
	if before.Path != path || before.Status != "ready" || saved.Revision == before.Revision || after.Revision != saved.Revision || len(after.Categories) != 1 {
		t.Fatalf("revision/readback: before=%+v saved=%+v after=%+v", before, saved, after)
	}
	c := after.Categories[0]
	if len(c.Models) != 2 || c.Models[0].Model != "custom/replaced" || !c.Models[0].Object || c.Models[1].Model != "vendor/first" || c.Reasoning == nil || *c.Reasoning != "high" {
		t.Fatalf("saved category: %+v", c)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("saved file: %s", b)
	for _, kept := range []string{`"temperature": 0.2`, `"other": {"keep": true}`, "// keep untouched"} {
		if !strings.Contains(string(b), kept) {
			t.Fatalf("lost %s: %s", kept, b)
		}
	}
	// A second save from the old page must fail without overwriting the saved result.
	res, err := server.Client().Post(server.URL+omoCategoriesURL, "application/json", strings.NewReader(`{"revision":"`+before.Revision+`","changes":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	failure, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stale POST: %d %s", res.StatusCode, failure)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("stale save: %d %s", res.StatusCode, failure)
	}
	kept, err := os.ReadFile(path)
	if err != nil || string(kept) != string(b) {
		t.Fatalf("stale save overwrote file: %s (%v)", kept, err)
	}
}

func TestOmoCategoriesRejectsInvalidChanges(t *testing.T) {
	for _, change := range []string{
		`{"scope":"unknown","name":"quick","reasoning":{"value":"high"}}`,
		`{"scope":"","name":"quick","reasoning":{"value":"high"}}`,
		`{"scope":null,"name":"quick","reasoning":{"value":"high"}}`,
		`{"scope":false,"name":"quick","reasoning":{"value":"high"}}`,
		`{"scope":"agents","name":"custom","models":[{"model":"p/a"}]}`,
		`{"scope":"agents","name":"explore","models":[{"model":"p/a","extra":true}]}`,
		`{"name":"unknown","reasoning":{"value":"high"}}`,
		`{"name":"quick","models":[]}`,
		`{"name":"quick","models":[{"model":" "}]}`,
		`{"name":"quick","reasoning":{"value":false}}`,
		`{"name":"quick","reasoning":{"value":" "}}`,
		`{"name":"quick","models":[{"model":"a","index":9}]}`,
		`{"name":"quick","reasoning":{"value":"high","unknown":true}}`,
	} {
		t.Run(change, func(t *testing.T) {
			path := omoCategoriesHome(t)
			if err := os.WriteFile(path, []byte(omoCategoriesFixture), 0o600); err != nil {
				t.Fatal(err)
			}
			doc, err := agent.ReadOmoCategories()
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, omoCategoriesURL, strings.NewReader(`{"revision":"`+doc.Revision+`","changes":[`+change+`]}`)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("invalid change: %d %s", rec.Code, rec.Body)
			}
			b, err := os.ReadFile(path)
			if err != nil || string(b) != omoCategoriesFixture {
				t.Fatalf("invalid change wrote file: %s (%v)", b, err)
			}
		})
	}
}
