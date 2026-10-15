// domain/sourcepolicy/novelty_test.go
package sourcepolicy_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/clustering"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/sourcepolicy"
)

func TestClassifyNovelty(t *testing.T) {
	cases := []struct {
		role clustering.MemberRole
		want sourcepolicy.NoveltyLevel
	}{
		{clustering.MemberRoleDuplicate, sourcepolicy.NoveltyNone},
		{clustering.MemberRoleConfirmation, sourcepolicy.NoveltyConfirmation},
		{clustering.MemberRoleSubstantialUpdate, sourcepolicy.NoveltySubstantial},
		{clustering.MemberRoleCorrection, sourcepolicy.NoveltyCorrection},
		{clustering.MemberRoleFirstReport, sourcepolicy.NoveltyMinor},
		{clustering.MemberRoleAnalysis, sourcepolicy.NoveltyMinor},
		{clustering.MemberRoleReaction, sourcepolicy.NoveltyMinor},
		{clustering.MemberRolePrimarySource, sourcepolicy.NoveltyMinor},
	}
	for _, c := range cases {
		if got := sourcepolicy.ClassifyNovelty(c.role); got != c.want {
			t.Errorf("ClassifyNovelty(%q) = %q, want %q", c.role, got, c.want)
		}
	}
}
