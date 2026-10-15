package editorialteacher

import (
	"testing"

	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

func TestValidateLabel(t *testing.T) {
	batch := map[string]bool{"a1": true, "a2": true}
	tests := []struct {
		name    string
		label   appout.EditorialTeacherLabel
		wantErr bool
	}{
		{"valid publish", appout.EditorialTeacherLabel{ArticleID: "a1", Label: "publish", Confidence: 0.9}, false},
		{"valid discard", appout.EditorialTeacherLabel{ArticleID: "a2", Label: "discard", Confidence: 0.1}, false},
		{"valid uncertain", appout.EditorialTeacherLabel{ArticleID: "a1", Label: "uncertain", Confidence: 0.5}, false},
		{"invalid label enum", appout.EditorialTeacherLabel{ArticleID: "a1", Label: "maybe", Confidence: 0.5}, true},
		{"confidence too high", appout.EditorialTeacherLabel{ArticleID: "a1", Label: "publish", Confidence: 1.5}, true},
		{"confidence negative", appout.EditorialTeacherLabel{ArticleID: "a1", Label: "publish", Confidence: -0.1}, true},
		{"article_id not in batch", appout.EditorialTeacherLabel{ArticleID: "a99", Label: "publish", Confidence: 0.9}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateLabel(tt.label, batch)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateLabel(%+v) error = %v, wantErr %v", tt.label, err, tt.wantErr)
			}
		})
	}
}
