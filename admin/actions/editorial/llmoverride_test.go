package editorial

import "testing"

func TestLLMOverride(t *testing.T) {
	for name, o := range map[string]interface{ LLMOverride() (string, string) }{
		"classify":      classifyParams{LLMBaseURL: "http://x/v1", LLMModel: "m"},
		"classify_eval": classifyEvalParams{LLMBaseURL: "http://x/v1", LLMModel: "m"},
	} {
		if u, m := o.LLMOverride(); u != "http://x/v1" || m != "m" {
			t.Errorf("%s: LLMOverride = %q, %q", name, u, m)
		}
	}
}
