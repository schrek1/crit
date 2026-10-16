package comment

import (
	"fmt"
	"strings"
	"time"

	"github.com/tomasz-tomczyk/crit/internal/review"
	"github.com/tomasz-tomczyk/crit/internal/session"
)

// runCommentStatus handles `crit comment --status seen|working [--timeout <d>] <id>...`:
// the agent reports where it is on comments sent to it. The status lands in
// the review file; a running daemon merges it like a reply.
func runCommentStatus(f commentFlags) error {
	if f.status != session.AgentStateSeen && f.status != session.AgentStateWorking {
		return fmt.Errorf("--status must be seen or working, got %q", f.status)
	}
	var timeout time.Duration
	if f.timeout != "" {
		d, err := time.ParseDuration(f.timeout)
		if err != nil || d <= 0 {
			return fmt.Errorf("--timeout must be a positive duration such as 45m, got %q", f.timeout)
		}
		timeout = d
	}
	if len(f.args) == 0 {
		return fmt.Errorf("--status needs at least one comment id")
	}
	if err := setAgentStatusInCritJSONAtPath(f.args, f.status, timeout, f.reviewPath); err != nil {
		return err
	}
	fmt.Printf("Status %s set on %d comment%s\n", f.status, len(f.args), plural(len(f.args)))
	return nil
}

// setAgentStatusInCritJSONAtPath sets the agent status on every comment in ids
// (file- or review-level). Nothing is written when any id is unknown.
func setAgentStatusInCritJSONAtPath(ids []string, state string, timeout time.Duration, critPath string) error {
	cj, err := review.LoadCritJSON(critPath)
	if err != nil {
		return err
	}
	now := time.Now()
	var missing []string
	for _, id := range ids {
		if !setAgentStatusOnComment(&cj, id, session.NewAgentStatus(state, timeout, now)) {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("comment %s not found in review file", strings.Join(missing, ", "))
	}
	return review.SaveCritJSON(critPath, cj)
}

func setAgentStatusOnComment(cj *session.CritJSON, id string, st *session.AgentStatus) bool {
	for i := range cj.ReviewComments {
		if cj.ReviewComments[i].ID == id {
			cj.ReviewComments[i].AgentStatus = st
			return true
		}
	}
	for path, cf := range cj.Files {
		for i := range cf.Comments {
			if cf.Comments[i].ID == id {
				cf.Comments[i].AgentStatus = st
				cj.Files[path] = cf
				return true
			}
		}
	}
	return false
}
