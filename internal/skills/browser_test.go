package skills

import "testing"

// TestSecretFieldsAreRefused is the guarantee that matters most in browser
// automation: credentials must not flow through a tool. The description says
// so, but a model that has talked itself into an exception should still hit a
// hard refusal in the code.
func TestSecretFieldsAreRefused(t *testing.T) {
	mustRefuse := []string{
		"#password", "input[type=password]", `input[type="password"]`,
		"#user_passwd", ".login-pwd", "#otp-code", "input[name=cvv]",
		"#card-number", "#ssn", "#api_token", "#mfa_code",
	}
	for _, sel := range mustRefuse {
		if !looksLikeSecretField(sel) {
			t.Errorf("would have typed into a credential field: %q", sel)
		}
	}

	mustAllow := []string{
		"#search", "input[name=query]", ".comment-box", "#email",
		"textarea#message", "#firstname",
	}
	for _, sel := range mustAllow {
		if looksLikeSecretField(sel) {
			t.Errorf("blocked an ordinary field: %q", sel)
		}
	}
}
