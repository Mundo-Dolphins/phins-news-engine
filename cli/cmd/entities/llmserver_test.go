package entities_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

func TestEntitiesDiscover_RejectsUnlistedLLMModel(t *testing.T) {
	testsupport.ExpectUnlistedLLMModel(t, testsupport.WriteFixture(t), "entities", "discover")
}

func TestEntitiesProposalsVerify_RejectsUnlistedLLMModel(t *testing.T) {
	testsupport.ExpectUnlistedLLMModel(t, testsupport.WriteFixture(t), "entities", "proposals", "verify")
}
