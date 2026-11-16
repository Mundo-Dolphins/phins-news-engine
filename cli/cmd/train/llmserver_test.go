package train_test

import (
	"testing"

	"github.com/Mundo-Dolphins/phins-news-engine/cli/cmd/testsupport"
)

func TestTrainMine_RejectsUnlistedLLMModel(t *testing.T) {
	testsupport.ExpectUnlistedLLMModel(t, testsupport.WriteFixture(t), "train", "mine")
}

func TestTrainRun_RejectsUnlistedLLMModel(t *testing.T) {
	testsupport.ExpectUnlistedLLMModel(t, testsupport.WriteFixture(t), "train", "run")
}
