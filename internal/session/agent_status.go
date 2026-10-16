package session

import "time"

// Agent status states stored on a comment. Replied and NotResponding are never
// stored: EffectiveAgentState derives them from replies and the deadline.
const (
	AgentStateQueued        = "queued"
	AgentStateDelivered     = "delivered"
	AgentStateSeen          = "seen"
	AgentStateWorking       = "working"
	AgentStateReplied       = "replied"
	AgentStateNotResponding = "not_responding"
)

// DefaultAgentTimeout is how long an agent may hold a delivered comment without
// reporting or replying before the comment shows "agent not responding".
const DefaultAgentTimeout = 15 * time.Minute

// AgentStatus tracks where a comment sent to an agent is. Timestamps are
// RFC3339Nano UTC; reports from the CLI and the daemon can land in the same
// second, so the disk merge needs sub-second order.
type AgentStatus struct {
	State      string `json:"state"`
	UpdatedAt  string `json:"updated_at"`
	DeadlineAt string `json:"deadline_at,omitempty"`
}

// EffectiveAgentState returns the state to show for c at now, or "" when the
// comment was never sent to an agent. A reply by anyone other than the
// comment's author after the last report counts as the agent's reply: crit
// does not know the agent's name in advance.
func EffectiveAgentState(c Comment, now time.Time) string {
	st := c.AgentStatus
	if st == nil {
		return ""
	}
	updated, err := time.Parse(time.RFC3339Nano, st.UpdatedAt)
	if err != nil {
		return st.State
	}
	// Reply timestamps carry whole seconds only.
	updated = updated.Truncate(time.Second)
	for _, r := range c.Replies {
		if r.Author == c.Author {
			continue
		}
		created, err := time.Parse(time.RFC3339, r.CreatedAt)
		if err == nil && !created.Before(updated) {
			return AgentStateReplied
		}
	}
	if st.DeadlineAt != "" && awaitsAgent(st.State) {
		deadline, err := time.Parse(time.RFC3339Nano, st.DeadlineAt)
		if err == nil && now.After(deadline) {
			return AgentStateNotResponding
		}
	}
	return st.State
}

func awaitsAgent(state string) bool {
	return state == AgentStateDelivered || state == AgentStateSeen || state == AgentStateWorking
}

// NewAgentStatus builds a status reported at now. Queued has no deadline;
// the other states get now+timeout, or DefaultAgentTimeout when timeout <= 0.
func NewAgentStatus(state string, timeout time.Duration, now time.Time) *AgentStatus {
	st := &AgentStatus{State: state, UpdatedAt: now.UTC().Format(time.RFC3339Nano)}
	if awaitsAgent(state) {
		if timeout <= 0 {
			timeout = DefaultAgentTimeout
		}
		st.DeadlineAt = now.Add(timeout).UTC().Format(time.RFC3339Nano)
	}
	return st
}

// SetCommentAgentStatus records an agent status on a comment; filePath ""
// targets a review-level comment. Notifies the browser on success.
func (s *Session) SetCommentAgentStatus(filePath, commentID, state string, timeout time.Duration) bool {
	st := NewAgentStatus(state, timeout, time.Now())
	s.mu.Lock()
	found := false
	if filePath == "" {
		for i := range s.reviewComments {
			if s.reviewComments[i].ID == commentID {
				s.reviewComments[i].AgentStatus = st
				found = true
				break
			}
		}
	} else if f, i := s.locateFileCommentLocked(filePath, commentID); f != nil {
		f.Comments[i].AgentStatus = st
		found = true
	}
	if found {
		s.scheduleWrite()
	}
	s.mu.Unlock()
	if found {
		s.notify(SSEEvent{Type: "comments-changed"})
	}
	return found
}

// newerAgentStatus reports whether disk should replace mem.
func newerAgentStatus(mem, disk *AgentStatus) bool {
	if disk == nil {
		return false
	}
	if mem == nil {
		return true
	}
	memTime, errMem := time.Parse(time.RFC3339Nano, mem.UpdatedAt)
	diskTime, errDisk := time.Parse(time.RFC3339Nano, disk.UpdatedAt)
	if errDisk != nil {
		return false
	}
	return errMem != nil || diskTime.After(memTime)
}
