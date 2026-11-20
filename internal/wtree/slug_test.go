package wtree

import (
	"strings"
	"testing"
)

func TestResolveSlug(t *testing.T) {
	cases := []struct {
		explicit string
		branch   string
		want     string
	}{
		{"", "feat/JIRA-12345-this-is-a-new-feature", "jira-12345"},
		{"", "feature/Add Something!", "feature-add-something"},
		{"CUSTOM-42", "ignored", "custom-42"},
		{"", strings.Repeat("a", 70), strings.Repeat("a", 63)},
		{"", strings.Repeat("a", 62) + " / bad", strings.Repeat("a", 62)},
	}
	for _, tc := range cases {
		got, err := ResolveSlug(tc.explicit, tc.branch)
		if err != nil || got != tc.want {
			t.Errorf("ResolveSlug(%q, %q) = %q, %v; want %q", tc.explicit, tc.branch, got, err, tc.want)
		}
	}
}

func TestValidateSlug(t *testing.T) {
	for _, explicit := range []string{"-bad", "bad-", "bad.slug", "bad/slug", strings.Repeat("a", 64)} {
		if _, err := ResolveSlug(explicit, "branch"); err == nil {
			t.Errorf("accepted %q", explicit)
		}
	}
	for _, branch := range []string{"", "///"} {
		if _, err := ResolveSlug("", branch); err == nil {
			t.Errorf("accepted branch %q", branch)
		}
	}
}