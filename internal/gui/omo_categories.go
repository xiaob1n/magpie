package gui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/yetone/magpie/internal/agent"
)

// omoCategoriesAPI 只编辑本机全局配置，路径由配置层决定。
func omoCategoriesAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents/omo/categories", func(rw http.ResponseWriter, r *http.Request) {
		doc, err := agent.ReadOmoCategories()
		omoCategoriesResponse(rw, doc, err)
	})
	mux.HandleFunc("POST /api/agents/omo/categories", func(rw http.ResponseWriter, r *http.Request) {
		var in *struct {
			Revision string                    `json:"revision"`
			Changes  []agent.OmoCategoryChange `json:"changes"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in == nil {
			fail(rw, errors.New("expected category change object"))
			return
		}
		if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("expected a single JSON object")
			}
			fail(rw, err)
			return
		}
		doc, err := agent.SaveOmoCategories(in.Revision, in.Changes)
		omoCategoriesResponse(rw, doc, err)
	})
}

func omoCategoriesResponse(rw http.ResponseWriter, doc agent.OmoCategories, err error) {
	if err == nil {
		writeJSON(rw, doc)
		return
	}
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, agent.ErrOmoCategoriesInput):
		code = http.StatusBadRequest
	case errors.Is(err, agent.ErrOmoCategoriesConflict):
		code = http.StatusConflict
	case errors.Is(err, agent.ErrOmoCategoriesMissing), errors.Is(err, os.ErrNotExist):
		code = http.StatusNotFound
	case errors.Is(err, os.ErrPermission):
		code = http.StatusForbidden
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	writeJSON(rw, struct {
		agent.OmoCategories
		Error string `json:"error"`
	}{doc, err.Error()})
}
