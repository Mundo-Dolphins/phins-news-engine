// engine/generated_rules_notice.go
package engine

import (
	"errors"
	"io/fs"
	"os"

	"github.com/Mundo-Dolphins/phins-news-engine/infra/clusteringrules"
)

// generatedRulesNotice describes a configured (non-empty) generated rules
// path for the startup log. missing is true when the file does not exist,
// which LoadWithGenerated treats as an empty set but is most likely a typo
// in clustering.generated_rules_path; merged is the number of generated
// phrases that made it into the rules (the file's phrases minus the ones
// the merge skipped, one warning each).
func generatedRulesNotice(path string, mergeWarnings []string) (missing bool, merged int, err error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return true, 0, nil
	}
	g, err := clusteringrules.ParseGenerated(path)
	if err != nil {
		return false, 0, err
	}
	return false, max(g.Len()-len(mergeWarnings), 0), nil
}
