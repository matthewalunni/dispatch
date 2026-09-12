package naming

import (
	"strings"
	"testing"
	"time"
)

func TestSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Implement exercise substitution", "implement-exercise-substitution"},
		{"  Review the current branch against main  ", "review-the-current-branch-against-main"},
		{"Fix: the API/v2 endpoint (urgent!)", "fix-the-api-v2-endpoint-urgent"},
		{"CamelCase Thing", "camelcase-thing"},
		{"multiple   spaces", "multiple-spaces"},
		{"---leading and trailing---", "leading-and-trailing"},
		{"日本語 only", "only"},
		{"", "task"},
		{"!!!", "task"},
		{"v2.1 rollout", "v2-1-rollout"},
	}
	for _, tc := range cases {
		if got := Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTruncateSlugBreaksOnWordBoundary(t *testing.T) {
	slug := "implement-exercise-substitution-for-the-workout-builder"
	got := TruncateSlug(slug, 30)
	if len(got) > 30 {
		t.Fatalf("TruncateSlug returned %d chars: %q", len(got), got)
	}
	if strings.HasSuffix(got, "-") {
		t.Errorf("TruncateSlug left a trailing dash: %q", got)
	}
	if got != "implement-exercise" {
		t.Errorf("TruncateSlug = %q, want a clean word boundary", got)
	}
}

func TestTruncateSlugShortInputUnchanged(t *testing.T) {
	if got := TruncateSlug("short", 40); got != "short" {
		t.Errorf("TruncateSlug(short) = %q", got)
	}
}

func TestBranchName(t *testing.T) {
	cases := []struct{ prefix, slug, want string }{
		{"dispatch/", "fix-login", "dispatch/fix-login"},
		{"dispatch", "fix-login", "dispatch/fix-login"},
		{"agent-", "fix-login", "agent-fix-login"},
		{"", "fix-login", "fix-login"},
	}
	for _, tc := range cases {
		if got := BranchName(tc.prefix, tc.slug); got != tc.want {
			t.Errorf("BranchName(%q,%q) = %q, want %q", tc.prefix, tc.slug, got, tc.want)
		}
	}
}

func TestAgentNameRespectsHerdrConstraints(t *testing.T) {
	long := Slug("Prototype an alternative onboarding flow for new members")
	name := AgentName("", long, nil)
	if !ValidAgentName(name) {
		t.Fatalf("AgentName produced an invalid herdr name: %q", name)
	}
	if len(name) > MaxAgentNameLen {
		t.Fatalf("AgentName produced %d chars: %q", len(name), name)
	}
}

func TestAgentNameAvoidsCollisions(t *testing.T) {
	taken := map[string]bool{"fix-login": true, "fix-login-2": true}
	name := AgentName("", "fix-login", func(n string) bool { return taken[n] })
	if taken[name] {
		t.Fatalf("AgentName reused a taken name: %q", name)
	}
	if name != "fix-login-3" {
		t.Errorf("AgentName = %q, want fix-login-3", name)
	}
}

func TestAgentNameCollisionStaysWithinLimit(t *testing.T) {
	base := Slug("Implement exercise substitution everywhere it matters")
	taken := func(string) bool { return true } // force the random fallback
	name := AgentName("", base, taken)
	if len(name) > MaxAgentNameLen || !ValidAgentName(name) {
		t.Fatalf("AgentName fallback produced %q (%d chars)", name, len(name))
	}
}

func TestAgentNameStartsWithALetter(t *testing.T) {
	// herdr requires [a-z] first; a numeric task title must not break that.
	name := AgentName("", Slug("2024 migration"), nil)
	if !ValidAgentName(name) {
		t.Fatalf("AgentName(%q) is not a valid herdr name", name)
	}
}

func TestValidAgentName(t *testing.T) {
	valid := []string{"a", "reviewer", "fix-login-2", "eng_task"}
	invalid := []string{"", "2fast", "-leading", "UPPER", "has space", strings.Repeat("a", 33)}
	for _, name := range valid {
		if !ValidAgentName(name) {
			t.Errorf("ValidAgentName(%q) = false, want true", name)
		}
	}
	for _, name := range invalid {
		if ValidAgentName(name) {
			t.Errorf("ValidAgentName(%q) = true, want false", name)
		}
	}
}

func TestTaskIDIsUniqueAndSortable(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 15, 0, 0, time.UTC)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := TaskID(now)
		if seen[id] {
			t.Fatalf("TaskID collided on %q", id)
		}
		seen[id] = true
		if !strings.HasPrefix(id, "task_20260912T101500_") {
			t.Fatalf("TaskID = %q, want a sortable timestamp prefix", id)
		}
	}
}
