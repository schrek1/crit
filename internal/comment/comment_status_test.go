package comment

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasz-tomczyk/crit/internal/daemon"
	"github.com/tomasz-tomczyk/crit/internal/session"
)

// seedStatusReview writes a review with one line comment (c_line on plan.md)
// and one review-level comment (r_general), and returns the --output dir and
// the review path RunComment resolves from it.
func seedStatusReview(t *testing.T) (outputDir, reviewPath string) {
	t.Helper()
	outputDir = t.TempDir()
	t.Chdir(outputDir)
	cwd, err := daemon.ResolvedCWD()
	if err != nil {
		t.Fatal(err)
	}
	reviewPath = filepath.Join(outputDir, "reviews", daemon.SessionKey(cwd, "", nil))
	if err := saveCritJSON(reviewPath, CritJSON{
		ReviewComments: []Comment{{ID: "r_general", Body: "general", Scope: "review", Author: "Ondra"}},
		Files: map[string]CritJSONFile{
			"plan.md": {Comments: []Comment{{ID: "c_line", Body: "why?", StartLine: 1, EndLine: 1, Author: "Ondra"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return outputDir, reviewPath
}

func loadedAgentStatus(t *testing.T, reviewPath, id string) *session.AgentStatus {
	t.Helper()
	cj, err := loadCritJSON(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cj.ReviewComments {
		if c.ID == id {
			return c.AgentStatus
		}
	}
	for _, f := range cj.Files {
		for _, c := range f.Comments {
			if c.ID == id {
				return c.AgentStatus
			}
		}
	}
	t.Fatalf("comment %s not in review", id)
	return nil
}

func TestCommentStatus_SetsSeen(t *testing.T) {
	outputDir, reviewPath := seedStatusReview(t)

	err := RunComment([]string{"--output", outputDir, "--status", "seen", "c_line"})

	if err != nil {
		t.Fatalf("RunComment: %v", err)
	}
	if st := loadedAgentStatus(t, reviewPath, "c_line"); st == nil || st.State != session.AgentStateSeen {
		t.Errorf("status = %+v, want seen", st)
	}
}

func TestCommentStatus_WorkingWithTimeout(t *testing.T) {
	outputDir, reviewPath := seedStatusReview(t)

	err := RunComment([]string{"--output", outputDir, "--status", "working", "--timeout", "45m", "c_line"})

	if err != nil {
		t.Fatalf("RunComment: %v", err)
	}
	st := loadedAgentStatus(t, reviewPath, "c_line")
	if st == nil || st.State != session.AgentStateWorking {
		t.Fatalf("status = %+v, want working", st)
	}
	updated, _ := time.Parse(time.RFC3339Nano, st.UpdatedAt)
	deadline, _ := time.Parse(time.RFC3339Nano, st.DeadlineAt)
	if deadline.Sub(updated) != 45*time.Minute {
		t.Errorf("deadline - updated = %v, want 45m", deadline.Sub(updated))
	}
}

func TestCommentStatus_MultipleIDs(t *testing.T) {
	outputDir, reviewPath := seedStatusReview(t)

	err := RunComment([]string{"--output", outputDir, "--status", "seen", "c_line", "r_general"})

	if err != nil {
		t.Fatalf("RunComment: %v", err)
	}
	for _, id := range []string{"c_line", "r_general"} {
		if st := loadedAgentStatus(t, reviewPath, id); st == nil || st.State != session.AgentStateSeen {
			t.Errorf("%s status = %+v, want seen", id, st)
		}
	}
}

func TestCommentStatus_RejectsUnknownState(t *testing.T) {
	outputDir, _ := seedStatusReview(t)

	err := RunComment([]string{"--output", outputDir, "--status", "done", "c_line"})

	if err == nil || !strings.Contains(err.Error(), "--status must be seen or working") {
		t.Errorf("err = %v, want the allowed-states message", err)
	}
}

func TestCommentStatus_UnknownIDErrors(t *testing.T) {
	outputDir, reviewPath := seedStatusReview(t)

	err := RunComment([]string{"--output", outputDir, "--status", "seen", "c_line", "c_missing"})

	if err == nil || !strings.Contains(err.Error(), "c_missing") {
		t.Errorf("err = %v, want an error naming c_missing", err)
	}
	if st := loadedAgentStatus(t, reviewPath, "c_line"); st != nil {
		t.Errorf("c_line status = %+v, want unchanged when an id is unknown", st)
	}
}

func TestCommentStatus_RequiresID(t *testing.T) {
	outputDir, _ := seedStatusReview(t)

	err := RunComment([]string{"--output", outputDir, "--status", "seen"})

	if err == nil || !strings.Contains(err.Error(), "comment id") {
		t.Errorf("err = %v, want a missing comment id error", err)
	}
}

func TestCommentStatus_RejectsBadTimeout(t *testing.T) {
	outputDir, _ := seedStatusReview(t)

	err := RunComment([]string{"--output", outputDir, "--status", "working", "--timeout", "soon", "c_line"})

	if err == nil || !strings.Contains(err.Error(), "--timeout") {
		t.Errorf("err = %v, want a --timeout error", err)
	}
}

func TestCommentStatus_RejectsJSONAndReplyTo(t *testing.T) {
	outputDir, _ := seedStatusReview(t)

	for _, extra := range [][]string{{"--json"}, {"--reply-to", "c_line"}} {
		args := append([]string{"--output", outputDir, "--status", "seen"}, extra...)
		err := RunComment(append(args, "c_line"))
		if err == nil || !strings.Contains(err.Error(), "--status cannot be used with") {
			t.Errorf("%v: err = %v, want a conflict error", extra, err)
		}
	}
}
