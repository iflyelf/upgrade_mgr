package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestQuickstart 返回中英双语、Linux/Windows 两套示例。
func TestQuickstart(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/quickstart", nil)
	rec := httptest.NewRecorder()
	Quickstart()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Version int `json:"version"`
			Title   struct {
				Zh string `json:"zh"`
				En string `json:"en"`
			} `json:"title"`
			Systems map[string]struct {
				Label struct {
					Zh string `json:"zh"`
					En string `json:"en"`
				} `json:"label"`
				Steps []struct {
					No    string `json:"no"`
					Title struct {
						Zh string `json:"zh"`
						En string `json:"en"`
					} `json:"title"`
					Lines struct {
						Zh []string `json:"zh"`
						En []string `json:"en"`
					} `json:"lines"`
				} `json:"steps"`
			} `json:"systems"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Version != 1 || out.Data.Title.Zh == "" || out.Data.Title.En == "" {
		t.Fatalf("基础字段不符: %+v", out.Data)
	}
	for _, key := range []string{"linux", "windows"} {
		sys, ok := out.Data.Systems[key]
		if !ok || len(sys.Steps) != 4 {
			t.Fatalf("%s 示例缺失或步数不是 4: %+v", key, sys)
		}
		for _, st := range sys.Steps {
			if st.Title.Zh == "" || st.Title.En == "" ||
				len(st.Lines.Zh) == 0 || len(st.Lines.En) == 0 {
				t.Fatalf("%s 步骤缺少中英文: %+v", key, st)
			}
		}
	}
}
