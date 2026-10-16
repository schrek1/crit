package prompt_test

import (
	"strings"
	"testing"

	"github.com/tomasz-tomczyk/crit/internal/prompt"
)

const skipAnsweredThreads = "Skip threads whose last reply is yours"

func renderStockUnresolved(mode, sessionMode string) string {
	return prompt.RenderFinish(nil, nil, "", "", false, prompt.Context{
		Mode:                   mode,
		NextRoundCmd:           "crit --session abcd",
		UnresolvedCount:        1,
		CommentsUnresolvedJSON: `[{"id":"c_1","body":"fix"}]`,
		InternalSessionMode:    sessionMode,
		PlanSlug:               "my-plan",
	}).Prompt
}

func TestStockFinishPrompt_MentionsAgentStatus(t *testing.T) {
	for _, mode := range []string{"files", "story"} {
		got := renderStockUnresolved(mode, "git")

		if !strings.Contains(got, "crit comment --status seen") || !strings.Contains(got, "crit comment --status working") {
			t.Errorf("%s prompt does not ask for status reports: %q", mode, got)
		}
		if !strings.Contains(got, skipAnsweredThreads) {
			t.Errorf("%s prompt does not say to skip answered threads: %q", mode, got)
		}
	}
}

func TestStockFinishPrompt_PlanSkipsAnsweredThreadsOnly(t *testing.T) {
	got := renderStockUnresolved("files", "plan")

	if strings.Contains(got, "--status") {
		t.Errorf("plan prompt should not ask for status reports: %q", got)
	}
	if !strings.Contains(got, skipAnsweredThreads) {
		t.Errorf("plan prompt does not say to skip answered threads: %q", got)
	}
}
