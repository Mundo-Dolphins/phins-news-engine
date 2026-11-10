package train_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/admin/action"
	"github.com/Mundo-Dolphins/phins-news-engine/admin/actions/train"
	"github.com/Mundo-Dolphins/phins-news-engine/app/datasetreview"
	"github.com/Mundo-Dolphins/phins-news-engine/app/editorialtrain"
)

// datasetFiles writes a temp dataset (a1 reject, a2 publish, a3 from the
// excluded source rss:official) and an audit CSV listing a1 and a2,
// returning the dataset, audit and out paths.
func datasetFiles(t *testing.T) (dataset, audit, out string) {
	t.Helper()
	dir := t.TempDir()
	dataset, audit, out = filepath.Join(dir, "d.yml"), filepath.Join(dir, "a.csv"), filepath.Join(dir, "r.yml")
	examples := []editorialtrain.TrainingExample{
		{ArticleID: "a1", Title: "One", Body: "B1", SourceID: "rss:feed-a", SourceType: "rss", Domain: "example.com", ContentType: "news", Label: "reject", LabelSource: "cli:label", Revision: 1},
		{ArticleID: "a2", Title: "Two", Body: "B2", SourceID: "rss:feed-a", Label: "publish", LabelSource: "cli:label", Revision: 1},
		{ArticleID: "a3", Title: "Three", Body: "B3", SourceID: "rss:official", Label: "publish", LabelSource: "cli:label", Revision: 1},
	}
	if err := editorialtrain.SaveDataset(dataset, examples); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audit, []byte("article_id,audit_reason\na1,r1\na2,r2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dataset, audit, out
}

func pathsJSON(dataset, audit, out string) string {
	return fmt.Sprintf(`"dataset":%q,"audit":%q,"out":%q`, dataset, audit, out)
}

func (f fixture) datasetFixture() (string, string, string, string) {
	f.t.Helper()
	f.cfg.Editorial.ModelBypassSourceIDs = []string{"rss:official"}
	d, a, o := datasetFiles(f.t)
	return d, a, o, pathsJSON(d, a, o)
}

func queueItems(t *testing.T, res action.Result) []map[string]any {
	t.Helper()
	raw, _ := json.Marshal(res.Object)
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got.Items
}

func TestDatasetActionsMetadata(t *testing.T) {
	for name, kind := range map[string]action.Kind{"train.review_dataset_queue": action.KindRead, "train.review_dataset_decide": action.KindWrite} {
		var a action.Action
		for _, c := range train.Actions() {
			if c.Name == name {
				a = c
			}
		}
		if a.Name == "" {
			t.Fatalf("missing %s", name)
		}
		if a.Group != "train" || a.Kind != kind || len(a.Modes) != 1 || a.Modes[0] != action.ModeLocal || a.Title == "" || a.Description == "" {
			t.Errorf("%s metadata = %+v", name, a)
		}
		props, _ := action.Schema(a.Params)["properties"].(map[string]any)
		for _, want := range []string{"dataset", "audit", "out"} {
			if props[want] == nil {
				t.Errorf("%s lacks param %s", name, want)
			}
		}
		for field, v := range props {
			if d, _ := v.(map[string]any)["description"].(string); d == "" {
				t.Errorf("%s param %s has no help", name, field)
			}
		}
	}
}

func TestDatasetQueuePendingOnly(t *testing.T) {
	f := newFixture(t)
	_, _, out, params := f.datasetFixture()
	res, err := f.run("train.review_dataset_queue", "{"+params+"}")
	if err != nil {
		t.Fatal(err)
	}
	items := queueItems(t, res)
	if len(items) != 2 || items[0]["article_id"] != "a1" || items[1]["article_id"] != "a2" {
		t.Fatalf("items = %+v, want a1,a2 (a3 is excluded)", items)
	}
	it := items[0]
	for k, want := range map[string]any{"audit_reason": "r1", "title": "One", "body": "B1", "source_id": "rss:feed-a",
		"source_type": "rss", "domain": "example.com", "content_type": "news", "label": "reject", "label_source": "cli:label"} {
		if it[k] != want {
			t.Errorf("%s = %v, want %v", k, it[k], want)
		}
	}
	if _, err := f.run("train.review_dataset_decide", `{`+params+`,"article_id":"a1","decision":"confirm"}`); err != nil {
		t.Fatal(err)
	}
	res, err = f.run("train.review_dataset_queue", "{"+params+"}")
	if err != nil {
		t.Fatal(err)
	}
	if items := queueItems(t, res); len(items) != 1 || items[0]["article_id"] != "a2" {
		t.Fatalf("after decision items = %+v, want only a2", items)
	}
	obj, _ := json.Marshal(res.Object)
	if !strings.Contains(string(obj), `"pending_count":1`) || !strings.Contains(string(obj), `"total":2`) || !strings.Contains(string(obj), out+".review.csv") {
		t.Errorf("object = %s", obj)
	}
}

func TestDatasetQueueURLFromDB(t *testing.T) {
	f := newFixture(t)
	f.seedReview("a1", "", 0.5) // a2 stays purged: no DB row
	_, _, _, params := f.datasetFixture()
	res, err := f.run("train.review_dataset_queue", "{"+params+"}")
	if err != nil {
		t.Fatal(err)
	}
	items := queueItems(t, res)
	if items[0]["url"] != "https://example.com/a1" {
		t.Errorf("a1 url = %v", items[0]["url"])
	}
	if items[1]["url"] != "" {
		t.Errorf("a2 url = %v, want empty", items[1]["url"])
	}
}

func TestDatasetDecideWritesCopyAndLog(t *testing.T) {
	for actor, want := range map[string]string{"user:ana": "ana", "mcp": "mcp"} {
		f := newFixture(t)
		dataset, _, out, params := f.datasetFixture()
		before, _ := os.ReadFile(dataset)
		res, err := f.runAs(actor, "train.review_dataset_decide", `{`+params+`,"article_id":"a1","decision":"publish"}`)
		if err != nil {
			t.Fatal(err)
		}
		if res.Summary != "a1: reject → publish" {
			t.Errorf("summary = %q", res.Summary)
		}
		obj, _ := json.Marshal(res.Object)
		if string(obj) != `{"article_id":"a1","previous_label":"reject","reviewed_label":"publish"}` {
			t.Errorf("object = %s", obj)
		}
		after, _ := os.ReadFile(dataset)
		if string(before) != string(after) {
			t.Error("source dataset modified")
		}
		ds, err := editorialtrain.LoadDataset(out)
		if err != nil {
			t.Fatal(err)
		}
		if ds.Examples[0].Label != "publish" || ds.Examples[0].LabelSource != "interactive_review" {
			t.Errorf("copy row = %+v", ds.Examples[0])
		}
		entries, err := datasetreview.ReadLog(out + ".review.csv")
		if err != nil || len(entries) != 1 || entries[0].Reviewer != want || entries[0].Audit != "r1" {
			t.Fatalf("log = %+v, %v; want reviewer %s", entries, err, want)
		}
		if _, err := os.Stat(out + ".manifest.json"); err != nil {
			t.Errorf("manifest: %v", err)
		}
	}
}

func TestDatasetDecideAlreadyReviewedIsConflict(t *testing.T) {
	f := newFixture(t)
	_, _, out, params := f.datasetFixture()
	body := `{` + params + `,"article_id":"a1","decision":"publish"}`
	if _, err := f.run("train.review_dataset_decide", body); err != nil {
		t.Fatal(err)
	}
	_, err := f.run("train.review_dataset_decide", body)
	wantCode(t, err, action.CodeConflict)
	if !strings.Contains(err.Error(), "already reviewed") {
		t.Errorf("err = %v", err)
	}
	if entries, _ := datasetreview.ReadLog(out + ".review.csv"); len(entries) != 1 {
		t.Errorf("log has %d entries, want 1", len(entries))
	}
}

func TestDatasetDecideNotInAuditIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, _, _, params := f.datasetFixture()
	_, err := f.run("train.review_dataset_decide", `{`+params+`,"article_id":"zz","decision":"publish"}`)
	wantCode(t, err, action.CodeNotFound)
}

func TestDatasetDecideMismatchedCopyIsConflict(t *testing.T) {
	f := newFixture(t)
	_, _, out, params := f.datasetFixture()
	if err := editorialtrain.SaveDataset(out, []editorialtrain.TrainingExample{{ArticleID: "a1", Title: "Other", SourceID: "rss:feed-a"}}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(out)
	_, err := f.run("train.review_dataset_decide", `{`+params+`,"article_id":"a1","decision":"publish"}`)
	wantCode(t, err, action.CodeConflict)
	if !strings.Contains(err.Error(), "use a new --out path") {
		t.Errorf("err = %v", err)
	}
	after, _ := os.ReadFile(out)
	if string(before) != string(after) {
		t.Error("copy changed")
	}
	if _, statErr := os.Stat(out + ".review.csv"); statErr == nil {
		t.Error("log written")
	}
	_, err = f.run("train.review_dataset_queue", "{"+params+"}")
	wantCode(t, err, action.CodeConflict)
}

func TestDatasetDecideSamePathIsInvalid(t *testing.T) {
	f := newFixture(t)
	d, a, _, _ := f.datasetFixture()
	before, _ := os.ReadFile(d)
	_, err := f.run("train.review_dataset_decide", `{`+pathsJSON(d, a, d)+`,"article_id":"a1","decision":"publish"}`)
	wantCode(t, err, action.CodeInvalidParams)
	if after, _ := os.ReadFile(d); string(before) != string(after) {
		t.Error("dataset modified")
	}
	_, err = f.run("train.review_dataset_queue", `{`+pathsJSON(d, a, d)+`}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestDatasetDecideBadDecision(t *testing.T) {
	f := newFixture(t)
	_, _, _, params := f.datasetFixture()
	_, err := f.run("train.review_dataset_decide", `{`+params+`,"article_id":"a1","decision":"skip"}`)
	wantCode(t, err, action.CodeInvalidParams)
}

func TestDatasetExcludedAuditRowIsConflict(t *testing.T) {
	f := newFixture(t)
	d, a, o, params := f.datasetFixture()
	if err := os.WriteFile(a, []byte("article_id,audit_reason\na1,r1\na3,r3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = d
	_, err := f.run("train.review_dataset_queue", "{"+params+"}")
	wantCode(t, err, action.CodeConflict)
	if !strings.Contains(err.Error(), "audit article a3 is absent from the dataset") {
		t.Errorf("err = %v", err)
	}
	_, err = f.run("train.review_dataset_decide", `{`+params+`,"article_id":"a1","decision":"publish"}`)
	wantCode(t, err, action.CodeConflict)
	if _, statErr := os.Stat(o); statErr == nil {
		t.Error("copy written")
	}
}

func TestDatasetMissingFileIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, a, _, params := f.datasetFixture()
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	_, err := f.run("train.review_dataset_queue", "{"+params+"}")
	wantCode(t, err, action.CodeNotFound)
	if !strings.Contains(err.Error(), a) {
		t.Errorf("err = %v, want it to name %s", err, a)
	}
}

func TestDatasetBadAuditIsInvalidParams(t *testing.T) {
	f := newFixture(t)
	_, a, _, params := f.datasetFixture()
	if err := os.WriteFile(a, []byte("id,why\na1,r1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.run("train.review_dataset_queue", "{"+params+"}")
	wantCode(t, err, action.CodeInvalidParams)
	if !strings.Contains(err.Error(), "article_id and audit_reason") {
		t.Errorf("err = %v", err)
	}
	if err := os.WriteFile(a, []byte("article_id,audit_reason\n ,r1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = f.run("train.review_dataset_queue", "{"+params+"}")
	wantCode(t, err, action.CodeInvalidParams)
}

func TestDatasetCorruptLogIsInvalidParams(t *testing.T) {
	f := newFixture(t)
	_, _, out, params := f.datasetFixture()
	if err := os.WriteFile(out+".review.csv", []byte("article_id,previous_label\na1,x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.run("train.review_dataset_queue", "{"+params+"}")
	wantCode(t, err, action.CodeInvalidParams)
	if !strings.Contains(err.Error(), "decision log") {
		t.Errorf("err = %v", err)
	}
}
