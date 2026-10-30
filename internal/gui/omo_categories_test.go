package gui

// allow: SIZE_OK — 用户限定的 API 验收文件集中覆盖真实路由，避免拆散已有隔离环境。

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
  // 保留未编辑的配置。
  "categories": {"quick": {"models": ["vendor/first", {"model": "vendor/second", "temperature": 0.2}], "reasoning": "low"}},
  "other": {"keep": true}
}`

func TestOmoCategoriesNamedAgentsHTTP(t *testing.T) {
	// 前提：真实路由读取没有 agent 覆盖的隔离配置。
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
	// 操作：通过同一文档同时保存 category 和具名 agent。
	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, omoCategoriesURL, strings.NewReader(`{"revision":"`+doc.Revision+`","changes":[{"scope":"categories","name":"quick","reasoning":{"value":"high"}},{"scope":"agents","name":"plan-consultant","models":[{"model":"p/planner"}],"reasoning":{"value":"custom"}}]}`)))
	var after agent.OmoCategories
	if err := json.Unmarshal(post.Body.Bytes(), &after); err != nil || post.Code != http.StatusOK || len(after.Agents) != 4 || after.Agents[2].Models[0].Model != "p/planner" || *after.Agents[2].Reasoning != "custom" || *after.Categories[0].Reasoning != "high" {
		t.Fatalf("POST agents: %d %s %v", post.Code, post.Body, err)
	}
	// 断言：再次 GET 与成功保存共享同一 revision。
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
			// 前提：真实配置位于隔离 HOME。
			path := omoCategoriesHome(t)
			if err := os.WriteFile(path, []byte(omoCategoriesFixture), 0o600); err != nil {
				t.Fatal(err)
			}
			doc, err := agent.ReadOmoCategories()
			if err != nil {
				t.Fatal(err)
			}
			// 操作：通过完整 Handler 提交非法请求。
			rec := httptest.NewRecorder()
			Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, omoCategoriesURL, strings.NewReader(strings.ReplaceAll(body, `"r"`, `"`+doc.Revision+`"`))))
			// 断言：返回可显示的错误，配置未被覆盖。
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
	// 前提：隔离 HOME 没有全局配置。
	omoCategoriesHome(t)
	// 操作：读取类别。
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, omoCategoriesURL, nil))
	// 断言：缺失配置与空类别明确区分。
	var out struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusNotFound || out.Error == "" {
		t.Fatalf("missing file: %d %s (%v)", rec.Code, rec.Body, err)
	}
}

func TestOmoCategoriesUsesHandlerProtection(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			// 前提：类别路由经过现有 Handler 防护。
			omoCategoriesHome(t)
			r := httptest.NewRequest(method, omoCategoriesURL, strings.NewReader(`{}`))
			r.Header.Set("Origin", "null")
			// 操作：不受信任的页面请求配置。
			rec := httptest.NewRecorder()
			Handler(nil, nil).ServeHTTP(rec, r)
			// 断言：在读取或写入之前拒绝。
			if rec.Code != http.StatusForbidden {
				t.Fatalf("untrusted page: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestOmoCategoriesUsesWebAuthentication(t *testing.T) {
	// 前提：浏览器端沿用现有认证包装。
	omoCategoriesHome(t)
	h := webGuard("categories", "secret", 0, Handler(nil, nil))
	// 操作：没有认证 cookie 的请求。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, omoCategoriesURL, nil))
	// 断言：未认证请求无法读取本机文件。
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d %s", rec.Code, rec.Body)
	}
}

func TestOmoCategoriesReadIOError(t *testing.T) {
	// 前提：配置路径是目录，不能作为文件读取。
	path := omoCategoriesHome(t)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	// 操作：通过真实 Handler 读取。
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, omoCategoriesURL, nil))
	// 断言：IO 错误不伪装成成功或空类别。
	var out struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusInternalServerError || out.Error == "" {
		t.Fatalf("read IO error: %d %s (%v)", rec.Code, rec.Body, err)
	}
}

func TestOmoCategoriesOnlyLocalRoute(t *testing.T) {
	for _, url := range []string{"/api/agents/codex/categories", "/api/agents/omo@wsl:Ubuntu/categories", "/api/agents/omo/categories/other"} {
		t.Run(url, func(t *testing.T) {
			// 前提：只允许固定的本机类别路由。
			path := omoCategoriesHome(t)
			if err := os.WriteFile(path, []byte(omoCategoriesFixture), 0o600); err != nil {
				t.Fatal(err)
			}
			// 操作：向非目标路由提交内容。
			rec := httptest.NewRecorder()
			Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{}`)))
			// 断言：请求不成功，也没有编辑本机文件。
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
	// 前提：真实 HTTP 服务和磁盘配置均位于隔离环境。
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
	// 操作：交换并编辑对象模型，保存后重新读取。
	saved := call(http.MethodPost, `{"revision":"`+before.Revision+`","changes":[{"name":"quick","models":[{"model":"custom/replaced","index":1},{"model":"vendor/first","index":0}],"reasoning":{"value":"high"}}]}`)
	after := call(http.MethodGet, "")
	// 断言：回读数据与磁盘一致，保留对象额外字段和未编辑区域。
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
	for _, kept := range []string{`"temperature": 0.2`, `"other": {"keep": true}`, "// 保留未编辑的配置。"} {
		if !strings.Contains(string(b), kept) {
			t.Fatalf("lost %s: %s", kept, b)
		}
	}
	// 旧页面的再次保存必须失败，不覆盖已保存结果。
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
