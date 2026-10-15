package train_test

import (
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

func TestTrainAnalyzeCmd_EmptyDatabase(t *testing.T) {
	configPath := testsupport.WriteFixture(t)

	out, err := testsupport.Execute(t, "--config", configPath, "train", "analyze")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out, "0 proposed") {
		t.Errorf("output = %q, want it to report 0 proposed on an empty database", out)
	}
}
