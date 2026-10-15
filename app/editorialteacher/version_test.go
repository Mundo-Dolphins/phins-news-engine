package editorialteacher

import "testing"

func TestPublicationPolicy_IsEmbedded(t *testing.T) {
	if PublicationPolicy == "" {
		t.Fatal("PublicationPolicy is empty — embed failed")
	}
	if PromptVersion == "" || PolicyVersion == "" {
		t.Fatal("PromptVersion/PolicyVersion must not be empty")
	}
}

func TestSetPublicationPolicy_OverridesAndRestores(t *testing.T) {
	t.Cleanup(func() { SetPublicationPolicy("", "") })
	if PolicyVersion != DefaultPolicyVersion {
		t.Fatalf("default PolicyVersion = %q, want %q", PolicyVersion, DefaultPolicyVersion)
	}
	SetPublicationPolicy("host-policy-v7", "host policy text")
	if PolicyVersion != "host-policy-v7" || PublicationPolicy != "host policy text" {
		t.Fatalf("override not applied: %q / %q", PolicyVersion, PublicationPolicy)
	}
	SetPublicationPolicy("", "")
	if PolicyVersion != DefaultPolicyVersion || PublicationPolicy != defaultPublicationPolicy {
		t.Fatal("empty values must restore the defaults")
	}
}
