package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/edit"
)

var (
	ErrOmoCategoriesInput    = errors.New("invalid Omo category change")
	ErrOmoCategoriesConflict = errors.New("Omo categories changed; reload before saving")
	ErrOmoCategoriesMissing  = errors.New("Omo category file does not exist")
	omoCategoriesMu          sync.Mutex
)

type OmoCategories struct {
	Path       string        `json:"path"`
	Revision   string        `json:"revision"`
	Status     string        `json:"status"`
	Categories []OmoCategory `json:"categories"`
	Agents     []OmoCategory `json:"agents"`
}

type OmoCategory struct {
	Name      string             `json:"name"`
	Status    string             `json:"status"`
	Error     string             `json:"error,omitempty"`
	Models    []OmoCategoryModel `json:"models"`
	Reasoning *string            `json:"reasoning,omitempty"`
}

type OmoCategoryModel struct {
	Model  string `json:"model"`
	Index  int    `json:"index"`
	Object bool   `json:"object"`
}

type OmoCategoryChange struct {
	Scope     OmoCategoryScope        `json:"scope,omitempty"`
	Name      string                  `json:"name"`
	Models    *[]OmoCategoryModelEdit `json:"models,omitempty"`
	Reasoning *OmoCategoryReasoning   `json:"reasoning,omitempty"`
}

type OmoCategoryScope string

// An omitted scope means categories; an explicit one must name one of the two collections.
func (s *OmoCategoryScope) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value != "categories" && value != "agents" {
		return fmt.Errorf("scope: %w", ErrOmoCategoriesInput)
	}
	*s = OmoCategoryScope(value)
	return nil
}

// Index refers to a position in the original list under the same revision; without it the model is added as a plain string, so a client can never set object parameters.
type OmoCategoryModelEdit struct {
	Model string `json:"model"`
	Index *int   `json:"index,omitempty"`
}

// A nil pointer leaves reasoning unchanged, a nil Value removes it, and any other value sets that non-empty string.
type OmoCategoryReasoning struct {
	Value *string `json:"value"`
}

// ReadOmoCategories reads the local global config only: no agent folder, and no legacy field migration.
func ReadOmoCategories() (OmoCategories, error) {
	doc, _, _, err := readOmoCategories()
	return doc, err
}

func readOmoCategories() (OmoCategories, []byte, gjson.Result, error) {
	doc := OmoCategories{Status: "error", Categories: []OmoCategory{}, Agents: []OmoCategory{}}
	home, err := os.UserHomeDir()
	if err != nil {
		return doc, nil, gjson.Result{}, err
	}
	doc.Path = filepath.Join(home, ".omo", "omo.jsonc")
	raw, err := os.ReadFile(doc.Path)
	if errors.Is(err, os.ErrNotExist) {
		p := filepath.Join(home, ".omo", "omo.json")
		raw, err = os.ReadFile(p)
		if !errors.Is(err, os.ErrNotExist) {
			doc.Path = p
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		doc.Status = "missing"
		return doc, nil, gjson.Result{}, ErrOmoCategoriesMissing
	}
	if err != nil {
		return doc, nil, gjson.Result{}, err
	}
	doc.Revision = fmt.Sprintf("%x", sha256.Sum256(raw))
	if !edit.ValidJSONC(raw) {
		return doc, nil, gjson.Result{}, fmt.Errorf("%s: invalid JSONC", doc.Path)
	}
	root := gjson.ParseBytes(jsonc.ToJSON(edit.TrimBOM(raw)))
	categories := root.Get("categories")
	if !root.IsObject() || (categories.Exists() && !categories.IsObject()) {
		return doc, nil, gjson.Result{}, fmt.Errorf("%s: expected categories object", doc.Path)
	}
	agents := root.Get("agents")
	if agents.Exists() && !agents.IsObject() {
		return doc, nil, gjson.Result{}, fmt.Errorf("%s: expected agents object", doc.Path)
	}
	categories.ForEach(func(key, value gjson.Result) bool {
		doc.Categories = append(doc.Categories, readOmoCategory(key.String(), value, false))
		return true
	})
	for _, name := range []string{"explore", "librarian", "plan-consultant", "plan-reviewer"} {
		doc.Agents = append(doc.Agents, readOmoCategory(name, agents.Get(name), true))
	}
	doc.Status = "ready"
	return doc, raw, root, nil
}

func readOmoCategory(name string, value gjson.Result, named bool) OmoCategory {
	c := OmoCategory{Name: name, Status: "ready", Models: []OmoCategoryModel{}}
	reasoning := value.Get("reasoning")
	models := value.Get("models")
	if named && !models.Exists() && value.Get("model").Exists() {
		models = gjson.Parse("[" + value.Get("model").Raw + "]")
	}
	switch {
	case named && !value.Exists():
	case !value.IsObject():
		c.Error = "expected category object"
	case named && !value.Get("models").Exists() && value.Get("model").Exists() && value.Get("model").Type != gjson.String:
		c.Error = "expected model string"
	case reasoning.Exists() && (reasoning.Type != gjson.String || strings.TrimSpace(reasoning.String()) == ""):
		c.Error = "expected nonempty reasoning string"
	case !models.Exists() && (value.Get("model").Exists() || value.Get("fallback_models").Exists()):
		c.Status = "legacy"
	case !models.Exists():
	case !models.IsArray():
		c.Error = "expected models array"
	default:
		for i, m := range models.Array() {
			id := m
			if m.IsObject() {
				id = m.Get("model")
			}
			if id.Type != gjson.String || strings.TrimSpace(id.String()) == "" {
				c.Error = fmt.Sprintf("models[%d]: expected nonempty model string or object", i)
				break
			}
			c.Models = append(c.Models, OmoCategoryModel{Model: id.String(), Index: i, Object: m.IsObject()})
		}
	}
	if c.Error != "" {
		c.Status = "error"
		c.Models = []OmoCategoryModel{}
	}
	if reasoning.Type == gjson.String {
		s := reasoning.String()
		c.Reasoning = &s
	}
	return c
}

// SaveOmoCategories serializes the read, revision check and write within this process; a writer outside that doesn't cooperate can still race the rename.
func SaveOmoCategories(revision string, changes []OmoCategoryChange) (OmoCategories, error) {
	omoCategoriesMu.Lock()
	defer omoCategoriesMu.Unlock()
	doc, raw, root, err := readOmoCategories()
	if err != nil {
		return doc, err
	}
	if len(revision) != 64 || strings.Trim(revision, "0123456789abcdef") != "" {
		return doc, fmt.Errorf("revision: %w", ErrOmoCategoriesInput)
	}
	if revision != doc.Revision {
		return doc, ErrOmoCategoriesConflict
	}
	var set []edit.KV
	var del []string
	seen := map[string]bool{}
	for _, change := range changes {
		scope := string(change.Scope)
		entries := doc.Categories
		switch scope {
		case "", "categories":
			scope = "categories"
		case "agents":
			entries = doc.Agents
		default:
			return doc, fmt.Errorf("scope: %w", ErrOmoCategoriesInput)
		}
		path := scope + "." + gjson.Escape(change.Name)
		var category *OmoCategory
		for i := range entries {
			if entries[i].Name == change.Name {
				category = &entries[i]
				break
			}
		}
		if category == nil || seen[path] || category.Status == "error" {
			return doc, fmt.Errorf("category %q: %w", change.Name, ErrOmoCategoriesInput)
		}
		seen[path] = true
		if change.Models != nil {
			if category.Status == "legacy" || len(*change.Models) == 0 {
				return doc, fmt.Errorf("category %q models: %w", change.Name, ErrOmoCategoriesInput)
			}
			original := root.Get(path + ".models").Array()
			if scope == "agents" && !root.Get(path+".models").Exists() && root.Get(path+".model").Exists() {
				original = []gjson.Result{root.Get(path + ".model")}
			}
			entries := make([]json.RawMessage, 0, len(*change.Models))
			used := map[int]bool{}
			for _, m := range *change.Models {
				if strings.TrimSpace(m.Model) == "" {
					return doc, fmt.Errorf("model: %w", ErrOmoCategoriesInput)
				}
				entry, err := json.Marshal(m.Model)
				if err != nil {
					return doc, err
				}
				if m.Index != nil {
					i := *m.Index
					if i < 0 || i >= len(original) || used[i] {
						return doc, fmt.Errorf("model index: %w", ErrOmoCategoriesInput)
					}
					used[i] = true
					if original[i].IsObject() {
						entry, err = edit.PatchJSON([]byte(original[i].Raw), []edit.KV{{Path: "model", Value: m.Model}}, nil)
						if err != nil {
							return doc, err
						}
					}
				}
				entries = append(entries, entry)
			}
			set = append(set, edit.KV{Path: path + ".models", Value: entries})
		}
		if r := change.Reasoning; r != nil {
			if r.Value == nil {
				del = append(del, path+".reasoning")
			} else {
				if strings.TrimSpace(*r.Value) == "" {
					return doc, fmt.Errorf("reasoning: %w", ErrOmoCategoriesInput)
				}
				set = append(set, edit.KV{Path: path + ".reasoning", Value: *r.Value})
			}
		}
	}
	out, err := edit.PatchJSON(raw, set, del)
	if err != nil {
		return doc, err
	}
	if !edit.ValidJSONC(out) {
		return doc, fmt.Errorf("invalid edited JSONC: %w", ErrOmoCategoriesInput)
	}
	if err := writeOmoCategories(doc, out); err != nil {
		return doc, err
	}
	return ReadOmoCategories()
}

// The snapshot is checked again before writing, shrinking the window for an outside change while the patch is computed; this check is not a filesystem CAS.
func writeOmoCategories(doc OmoCategories, out []byte) error {
	current, err := os.ReadFile(doc.Path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrOmoCategoriesMissing
	}
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(current)) != doc.Revision {
		return ErrOmoCategoriesConflict
	}
	return edit.WriteAtomic(doc.Path, out)
}
