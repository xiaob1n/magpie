package agent

// allow: SIZE_OK — 配置层读写验收集中于用户限定的唯一测试文件，按行为拆分测试而不新增文件。

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/testenv"
)

func omoCategoriesFile(t *testing.T, text string) string {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	p := filepath.Join(home, ".omo", "omo.jsonc")
	file(t, p, text)
	return p
}

func TestOmoCategoriesNamedAgentsAbsent(t *testing.T) {
	// 前提：没有内置覆盖，读取不能生成运行时默认值。
	p := omoCategoriesFile(t, `{}`)
	doc, err := ReadOmoCategories()
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"explore", "librarian", "plan-consultant", "plan-reviewer"} {
		if len(doc.Agents) != 4 || doc.Agents[i].Name != name || doc.Agents[i].Status != "ready" || len(doc.Agents[i].Models) != 0 || doc.Agents[i].Reasoning != nil {
			t.Fatalf("absent agents: %+v", doc.Agents)
		}
	}
	// 操作：空保存不创建任何覆盖。
	if _, err := SaveOmoCategories(doc.Revision, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != `{}` {
		t.Fatalf("implicit write: %s (%v)", b, err)
	}
}

func TestOmoCategoriesNamedAgentsAtomicRoundtrip(t *testing.T) {
	// 前提：相同名字分属两个集合，保留自定义 agent 和对象参数。
	p := omoCategoriesFile(t, `{"categories":{"explore":{"models":["category/old"]}},"agents":{"explore":{"models":[{"model":"agent/old","extra":17}],"prompt":"keep"},"custom":{"models":false}},"other":true}`)
	doc, err := ReadOmoCategories()
	if err != nil {
		t.Fatal(err)
	}
	i := 0
	models := []OmoCategoryModelEdit{{Model: "agent/new", Index: &i}}
	category := []OmoCategoryModelEdit{{Model: "category/new"}}
	added := []OmoCategoryModelEdit{{Model: "agent/added"}}
	// 操作：一次保存两个集合，同时显式创建缺少的内置覆盖。
	after, err := SaveOmoCategories(doc.Revision, []OmoCategoryChange{
		{Name: "explore", Models: &category},
		{Scope: "agents", Name: "explore", Models: &models},
		{Scope: "agents", Name: "librarian", Models: &added},
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.Categories[0].Models[0].Model != "category/new" || after.Agents[0].Models[0].Model != "agent/new" || !after.Agents[0].Models[0].Object || after.Agents[1].Models[0].Model != "agent/added" {
		t.Fatalf("scoped roundtrip: %+v", after)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{`"extra":17`, `"prompt":"keep"`, `"custom":{"models":false}`, `"other":true`} {
		if !strings.Contains(string(b), keep) {
			t.Fatalf("lost %s: %s", keep, b)
		}
	}
	if _, err := SaveOmoCategories(doc.Revision, []OmoCategoryChange{{Scope: "agents", Name: "plan-reviewer", Models: &added}}); !errors.Is(err, ErrOmoCategoriesConflict) {
		t.Fatalf("stale cross-scope save: %v", err)
	}
}

func TestOmoCategoriesNamedAgentsLegacy(t *testing.T) {
	// 前提：单模型旧输入仍是可编辑覆盖，未知字段不能被迁移删除。
	p := omoCategoriesFile(t, `{"agents":{"explore":{"model":"old/model","extra":7,"reasoning":"low"}}}`)
	doc, err := ReadOmoCategories()
	if err != nil || doc.Agents[0].Status != "ready" || len(doc.Agents[0].Models) != 1 || doc.Agents[0].Models[0].Model != "old/model" {
		t.Fatalf("legacy read: %+v %v", doc, err)
	}
	i := 0
	models := []OmoCategoryModelEdit{{Model: "new/model", Index: &i}}
	// 操作：按读取的索引编辑并清除推理覆盖。
	after, err := SaveOmoCategories(doc.Revision, []OmoCategoryChange{{Scope: "agents", Name: "explore", Models: &models, Reasoning: &OmoCategoryReasoning{}}})
	if err != nil || after.Agents[0].Models[0].Model != "new/model" || after.Agents[0].Reasoning != nil {
		t.Fatalf("legacy save: %+v %v", after, err)
	}
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), `"model":"old/model"`) || !strings.Contains(string(b), `"extra":7`) {
		t.Fatalf("legacy fields lost: %s %v", b, err)
	}
}

func TestOmoCategoriesMalformedAgents(t *testing.T) {
	for _, value := range []string{`null`, `[]`, `false`, `"bad"`} {
		t.Run(value, func(t *testing.T) {
			// 操作：顶层 agents 非对象不能读取或被任何保存覆盖。
			raw := `{"agents":` + value + `}`
			p := omoCategoriesFile(t, raw)
			if _, err := ReadOmoCategories(); err == nil {
				t.Fatal("malformed agents accepted")
			}
			if _, err := SaveOmoCategories(fmt.Sprintf("%x", sha256.Sum256([]byte(raw))), nil); err == nil {
				t.Fatal("malformed agents overwritten")
			}
			b, err := os.ReadFile(p)
			if err != nil || string(b) != raw {
				t.Fatalf("changed file: %s %v", b, err)
			}
		})
	}
}

func TestOmoCategoriesNamedAgentInvalidEditIsAtomic(t *testing.T) {
	for _, change := range []OmoCategoryChange{
		{Scope: "other", Name: "explore"},
		{Scope: "agents", Name: "custom"},
		{Scope: "agents", Name: "plan-reviewer"},
	} {
		t.Run(string(change.Scope)+change.Name, func(t *testing.T) {
			// 前提：批次前一项有效，后一项指向非法集合、名字或损坏行。
			raw := `{"categories":{"a":{}},"agents":{"plan-reviewer":null}}`
			p := omoCategoriesFile(t, raw)
			doc, err := ReadOmoCategories()
			if err != nil || doc.Agents[3].Status != "error" || len(doc.Agents[3].Models) != 0 {
				t.Fatalf("malformed row: %+v %v", doc, err)
			}
			high := "high"
			change.Reasoning = &OmoCategoryReasoning{Value: &high}
			// 操作：非法项拒绝整个批次。
			_, err = SaveOmoCategories(doc.Revision, []OmoCategoryChange{{Name: "a", Reasoning: &OmoCategoryReasoning{Value: &high}}, change})
			if !errors.Is(err, ErrOmoCategoriesInput) {
				t.Fatal(err)
			}
			b, err := os.ReadFile(p)
			if err != nil || string(b) != raw {
				t.Fatalf("partial write: %s %v", b, err)
			}
		})
	}
}

func TestOmoCategoriesNamedAgentMalformedRows(t *testing.T) {
	for _, value := range []string{`null`, `{"model":{"model":"not-a-scalar"}}`, `{"models":false}`, `{"models":[null]}`, `{"models":[" "]}`, `{"reasoning":false}`} {
		t.Run(value, func(t *testing.T) {
			// 前提：损坏的具名行不能伪装成缺省行。
			omoCategoriesFile(t, `{"agents":{"explore":`+value+`}}`)
			doc, err := ReadOmoCategories()
			if err != nil || doc.Agents[0].Status != "error" || doc.Agents[0].Error == "" || len(doc.Agents[0].Models) != 0 {
				t.Fatalf("malformed row: %+v %v", doc, err)
			}
			// 操作：损坏行的模型和推理都拒绝编辑。
			high := "high"
			if _, err := SaveOmoCategories(doc.Revision, []OmoCategoryChange{{Scope: "agents", Name: "explore", Reasoning: &OmoCategoryReasoning{Value: &high}}}); !errors.Is(err, ErrOmoCategoriesInput) {
				t.Fatal(err)
			}
		})
	}
}

func TestOmoCategoriesRead(t *testing.T) {
	// 给定带 BOM、对象模型和动态类别名的配置。
	raw := "\xef\xbb\xbf{\r\n // keep\r\n \"categories\": {"
	for i := range 9 {
		if i > 0 {
			raw += ","
		}
		raw += fmt.Sprintf(`"category.%d":{"models":["p/a",{"model":"p/b","temperature":0.3}],"reasoning":"custom"}`, i)
	}
	raw += "},\r\n}\r\n"
	p := omoCategoriesFile(t, raw)
	t.Setenv("OMO_CODING_AGENT_DIR", t.TempDir())
	file(t, strings.TrimSuffix(p, "c"), `{"categories":{}}`)
	got, err := ReadOmoCategories()
	// 读取真实磁盘，版本包含 BOM，且不受 agent 目录覆盖影响。
	if err != nil || got.Status != "ready" || got.Path != p || got.Revision != fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) || len(got.Categories) != 9 {
		t.Fatalf("%+v: %v", got, err)
	}
	c := got.Categories[0]
	if c.Name != "category.0" || c.Models[1].Model != "p/b" || !c.Models[1].Object || c.Models[1].Index != 1 || *c.Reasoning != "custom" {
		t.Fatalf("%+v", c)
	}
}

func TestOmoCategoriesReadFailures(t *testing.T) {
	for _, text := range []string{"", `{`, `[]`, `{"categories":[]}`, `{"categories":null}`} {
		t.Run(text, func(t *testing.T) {
			omoCategoriesFile(t, text)
			got, err := ReadOmoCategories()
			if err == nil || got.Status != "error" {
				t.Fatalf("%+v: %v", got, err)
			}
		})
	}
}

func TestOmoCategoriesMalformedModelsAreReadOnly(t *testing.T) {
	for _, value := range []string{`[null]`, `[42]`, `[{}]`, `[{"model":false}]`, `[" "]`, `["good",false]`, `[]`} {
		t.Run(value, func(t *testing.T) {
			omoCategoriesFile(t, `{"categories":{"a":{"models":`+value+`}}}`)
			got, err := ReadOmoCategories()
			if err != nil {
				t.Fatal(err)
			}
			if value == `[]` {
				if len(got.Categories[0].Models) != 0 {
					t.Fatal(got)
				}
				return
			}
			if got.Categories[0].Status != "error" || got.Categories[0].Error == "" {
				t.Fatal(got)
			}
			s := "low"
			_, err = SaveOmoCategories(got.Revision, []OmoCategoryChange{{Name: "a", Reasoning: &OmoCategoryReasoning{Value: &s}}})
			if !errors.Is(err, ErrOmoCategoriesInput) {
				t.Fatal(err)
			}
		})
	}
}

func TestOmoCategoriesMissingAndFallback(t *testing.T) {
	p := omoCategoriesFile(t, `{}`)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	got, err := ReadOmoCategories()
	if !errors.Is(err, ErrOmoCategoriesMissing) || got.Status != "missing" {
		t.Fatalf("%+v %v", got, err)
	}
	file(t, strings.TrimSuffix(p, "c"), `{}`)
	got, err = ReadOmoCategories()
	if err != nil || got.Path != strings.TrimSuffix(p, "c") || len(got.Categories) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestOmoCategoriesMalformedAndLegacy(t *testing.T) {
	omoCategoriesFile(t, `{"categories":{"bad":{"models":42},"legacy":{"model":"p/a","fallback_models":["p/b"]},"wrong":null}}`)
	got, err := ReadOmoCategories()
	if err != nil || got.Categories[0].Status != "error" || got.Categories[1].Status != "legacy" || got.Categories[2].Status != "error" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestOmoCategoriesDiskRoundtrip(t *testing.T) {
	// 给定保留区域、对象参数、BOM 和 CRLF。
	raw := "\xef\xbb\xbf{\r\n // keep\r\n \"_migrations\": {\"x\":true},\r\n \"categories\": {\r\n  \"a.b\": {\"models\":[\"p/a\",{\"model\":\"p/b\",\"extra\":{\"n\":2}}],\"reasoning\":\"high\",\"prompt\":\"keep\"},\r\n  \"other\": { \"models\": [\"p/c\"] },\r\n },\r\n}\r\n"
	p := omoCategoriesFile(t, raw)
	before, err := ReadOmoCategories()
	if err != nil {
		t.Fatal(err)
	}
	i := 1
	models := []OmoCategoryModelEdit{{Model: "p/new", Index: &i}, {Model: "p/add"}}
	low := "low"
	got, err := SaveOmoCategories(before.Revision, []OmoCategoryChange{{Name: "a.b", Models: &models, Reasoning: &OmoCategoryReasoning{}}, {Name: "other", Reasoning: &OmoCategoryReasoning{Value: &low}}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"\xef\xbb\xbf", "// keep", `"_migrations": {"x":true}`, `"prompt":"keep"`, `"extra"`, `"n": 2`} {
		if !strings.Contains(string(b), keep) {
			t.Fatalf("lost %q: %s", keep, b)
		}
	}
	if !edit.ValidJSONC(b) || strings.Count(string(b), "\n") != strings.Count(string(b), "\r\n") || got.Categories[0].Models[0].Model != "p/new" || !got.Categories[0].Models[0].Object || got.Categories[0].Reasoning != nil || got.Revision == before.Revision {
		t.Fatalf("%+v\n%s", got, b)
	}
	read, err := ReadOmoCategories()
	if err != nil || read.Revision != got.Revision {
		t.Fatalf("%+v %v", read, err)
	}
	t.Logf("disk read-save-read: status=%s models=%+v revision changed=%v; BOM/CRLF/comments/extras retained", read.Status, read.Categories[0].Models, read.Revision != before.Revision)
}

func TestOmoCategoriesRejectChanges(t *testing.T) {
	for _, kind := range []string{"empty", "blank", "index", "duplicate-index", "category", "duplicate-category", "reasoning", "legacy", "malformed", "revision"} {
		t.Run(kind, func(t *testing.T) {
			raw := `{"categories":{"a":{"models":[{"model":"p/a","extra":1}]},"legacy":{"model":"p/a"},"bad":{"models":2}}}`
			p := omoCategoriesFile(t, raw)
			got, err := ReadOmoCategories()
			if err != nil {
				t.Fatal(err)
			}
			models := []OmoCategoryModelEdit{{Model: "p/b"}}
			c := OmoCategoryChange{Name: "a", Models: &models}
			rev := got.Revision
			want := ErrOmoCategoriesInput
			switch kind {
			case "empty":
				models = []OmoCategoryModelEdit{}
			case "blank":
				models[0].Model = " "
			case "index":
				i := 3
				models[0].Index = &i
			case "duplicate-index":
				i := 0
				models = []OmoCategoryModelEdit{{Model: "p/b", Index: &i}, {Model: "p/c", Index: &i}}
			case "category":
				c.Name = "unknown"
			case "reasoning":
				s := " "
				c.Reasoning = &OmoCategoryReasoning{Value: &s}
			case "legacy":
				c.Name = "legacy"
			case "malformed":
				c.Name = "bad"
			case "revision":
				rev = strings.Repeat("0", 64)
				want = ErrOmoCategoriesConflict
			}
			changes := []OmoCategoryChange{c}
			if kind == "duplicate-category" {
				changes = append(changes, c)
			}
			_, err = SaveOmoCategories(rev, changes)
			if !errors.Is(err, want) {
				t.Fatalf("%v want %v", err, want)
			}
			if b, err := os.ReadFile(p); err != nil || string(b) != raw {
				t.Fatalf("changed file: %s %v", b, err)
			}
		})
	}
}

func TestOmoCategoriesConcurrentConflict(t *testing.T) {
	omoCategoriesFile(t, `{"categories":{"a":{"models":["p/a"]}}}`)
	got, err := ReadOmoCategories()
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, value := range []string{"low", "high"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := SaveOmoCategories(got.Revision, []OmoCategoryChange{{Name: "a", Reasoning: &OmoCategoryReasoning{Value: &value}}})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrOmoCategoriesConflict):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestOmoCategoriesPrewriteSnapshot(t *testing.T) {
	for _, kind := range []string{"changed", "missing"} {
		t.Run(kind, func(t *testing.T) {
			p := omoCategoriesFile(t, `{"categories":{"a":{"models":["p/a"]}}}`)
			before, err := ReadOmoCategories()
			if err != nil {
				t.Fatal(err)
			}
			external := "\xef\xbb\xbf" + `{"categories":{"a":{"models":["p/a"]}}}`
			want := ErrOmoCategoriesConflict
			if kind == "missing" {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				want = ErrOmoCategoriesMissing
			} else {
				file(t, p, external)
			}
			err = writeOmoCategories(before, []byte(`{"categories":{}}`))
			if !errors.Is(err, want) {
				t.Fatalf("%v want %v", err, want)
			}
			b, err := os.ReadFile(p)
			if kind == "missing" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing file recreated: %s %v", b, err)
				}
			} else if err != nil || string(b) != external {
				t.Fatalf("external bytes changed: %s %v", b, err)
			}
		})
	}
}

func TestOmoCategoriesExternalConflict(t *testing.T) {
	p := omoCategoriesFile(t, `{"categories":{"a":{"models":["p/a"]}}}`)
	before, err := ReadOmoCategories()
	if err != nil {
		t.Fatal(err)
	}
	external := `{"categories":{"a":{"models":["external"]}}}`
	file(t, p, external)
	_, err = SaveOmoCategories(before.Revision, nil)
	if !errors.Is(err, ErrOmoCategoriesConflict) {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != external {
		t.Fatalf("%s %v", b, err)
	}
}

func TestOmoCategoriesLegacyReasoningAndEscapedKeys(t *testing.T) {
	for _, name := range []string{"a.b", `a\b`, "a*b", "a?b", "a#b", `a"b`} {
		t.Run(name, func(t *testing.T) {
			key, err := json.Marshal(name)
			if err != nil {
				t.Fatal(err)
			}
			p := omoCategoriesFile(t, `{"categories":{`+string(key)+`:{"model":"old","fallback_models":["fallback"],"reasoning":"high"},"untouched": { "models": ["x"] }}}`)
			before, err := ReadOmoCategories()
			if err != nil {
				t.Fatal(err)
			}
			s := "custom"
			got, err := SaveOmoCategories(before.Revision, []OmoCategoryChange{{Name: name, Reasoning: &OmoCategoryReasoning{Value: &s}}})
			if err != nil || got.Categories[0].Status != "legacy" || *got.Categories[0].Reasoning != s {
				t.Fatalf("%+v %v", got, err)
			}
			b, err := os.ReadFile(p)
			if err != nil || !strings.Contains(string(b), `"untouched": { "models": ["x"] }`) || !strings.Contains(string(b), `"fallback_models":["fallback"]`) {
				t.Fatalf("%s %v", b, err)
			}
		})
	}
}

func TestOmoCategoriesIOFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission fixture")
	}
	for _, kind := range []string{"read", "write", "disappeared"} {
		t.Run(kind, func(t *testing.T) {
			p := omoCategoriesFile(t, `{"categories":{"a":{"models":["p/a"]}}}`)
			before, err := ReadOmoCategories()
			if err != nil {
				t.Fatal(err)
			}
			s := "low"
			switch kind {
			case "read":
				if err := os.Chmod(p, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(p, 0o644) })
				got, err := ReadOmoCategories()
				if err == nil || got.Status != "error" {
					t.Fatalf("%+v %v", got, err)
				}
				return
			case "write":
				if err := os.Chmod(filepath.Dir(p), 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(filepath.Dir(p), 0o755) })
			case "disappeared":
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			}
			_, err = SaveOmoCategories(before.Revision, []OmoCategoryChange{{Name: "a", Reasoning: &OmoCategoryReasoning{Value: &s}}})
			if err == nil {
				t.Fatal("unexpected successful save")
			}
			if kind == "disappeared" && !errors.Is(err, ErrOmoCategoriesMissing) {
				t.Fatal(err)
			}
			if kind == "write" {
				b, e := os.ReadFile(p)
				if e != nil || fmt.Sprintf("%x", sha256.Sum256(b)) != before.Revision {
					t.Fatalf("write failure changed file: %s %v", b, e)
				}
			}
		})
	}
}
