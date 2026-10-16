package session

import (
	"testing"
	"time"
)

func agentStatusAt(state string, updated time.Time, timeout time.Duration) *AgentStatus {
	st := &AgentStatus{State: state, UpdatedAt: updated.UTC().Format(time.RFC3339Nano)}
	if timeout > 0 {
		st.DeadlineAt = updated.Add(timeout).UTC().Format(time.RFC3339Nano)
	}
	return st
}

func TestEffectiveAgentState_Replied(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	c := Comment{
		Author:      "Ondra",
		AgentStatus: agentStatusAt(AgentStateWorking, t0, DefaultAgentTimeout),
		Replies: []Reply{
			{Author: "Claude Code", CreatedAt: t0.Add(time.Second).Format(time.RFC3339)},
		},
	}

	got := EffectiveAgentState(c, t0.Add(2*time.Second))

	if got != AgentStateReplied {
		t.Errorf("EffectiveAgentState = %q, want %q", got, AgentStateReplied)
	}
}

func TestEffectiveAgentState_RepliedWithinSameSecond(t *testing.T) {
	// Reply timestamps carry whole seconds only, so a reply in the same second
	// as the status report must still count.
	t0 := time.Date(2026, 10, 16, 10, 0, 0, 700_000_000, time.UTC)
	c := Comment{
		Author:      "Ondra",
		AgentStatus: agentStatusAt(AgentStateWorking, t0, DefaultAgentTimeout),
		Replies: []Reply{
			{Author: "Claude Code", CreatedAt: t0.Truncate(time.Second).Format(time.RFC3339)},
		},
	}

	got := EffectiveAgentState(c, t0.Add(time.Second))

	if got != AgentStateReplied {
		t.Errorf("EffectiveAgentState = %q, want %q", got, AgentStateReplied)
	}
}

func TestEffectiveAgentState_ReviewerReplyIsNotReplied(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	c := Comment{
		Author:      "Ondra",
		AgentStatus: agentStatusAt(AgentStateWorking, t0, DefaultAgentTimeout),
		Replies: []Reply{
			{Author: "Ondra", CreatedAt: t0.Add(time.Second).Format(time.RFC3339)},
		},
	}

	got := EffectiveAgentState(c, t0.Add(2*time.Second))

	if got != AgentStateWorking {
		t.Errorf("EffectiveAgentState = %q, want %q", got, AgentStateWorking)
	}
}

func TestEffectiveAgentState_NotResponding(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	c := Comment{Author: "Ondra", AgentStatus: agentStatusAt(AgentStateDelivered, t0, DefaultAgentTimeout)}

	if got := EffectiveAgentState(c, t0.Add(16*time.Minute)); got != AgentStateNotResponding {
		t.Errorf("after deadline: EffectiveAgentState = %q, want %q", got, AgentStateNotResponding)
	}
	if got := EffectiveAgentState(c, t0.Add(14*time.Minute)); got != AgentStateDelivered {
		t.Errorf("before deadline: EffectiveAgentState = %q, want %q", got, AgentStateDelivered)
	}
}

func TestEffectiveAgentState_QueuedHasNoDeadline(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	c := Comment{Author: "Ondra", AgentStatus: agentStatusAt(AgentStateQueued, t0, 0)}

	got := EffectiveAgentState(c, t0.Add(time.Hour))

	if got != AgentStateQueued {
		t.Errorf("EffectiveAgentState = %q, want %q", got, AgentStateQueued)
	}
}

func TestEffectiveAgentState_NoStatus(t *testing.T) {
	if got := EffectiveAgentState(Comment{Author: "Ondra"}, time.Now()); got != "" {
		t.Errorf("EffectiveAgentState = %q, want empty", got)
	}
}

func TestSetCommentAgentStatus_DefaultTimeout(t *testing.T) {
	s := newTestSession(t)
	c, ok := s.AddComment("plan.md", 1, 1, "", "first", "", "Ondra", "")
	if !ok {
		t.Fatal("AddComment failed")
	}

	if !s.SetCommentAgentStatus("plan.md", c.ID, AgentStateDelivered, 0) {
		t.Fatal("SetCommentAgentStatus returned false")
	}

	got, _, _ := s.FindCommentByID(c.ID, "plan.md")
	st := got.AgentStatus
	if st == nil || st.State != AgentStateDelivered {
		t.Fatalf("AgentStatus = %+v, want state %q", st, AgentStateDelivered)
	}
	updated, _ := time.Parse(time.RFC3339Nano, st.UpdatedAt)
	deadline, _ := time.Parse(time.RFC3339Nano, st.DeadlineAt)
	if deadline.Sub(updated) != DefaultAgentTimeout {
		t.Errorf("deadline - updated = %v, want %v", deadline.Sub(updated), DefaultAgentTimeout)
	}
}

func TestSetCommentAgentStatus_QueuedHasNoDeadline(t *testing.T) {
	s := newTestSession(t)
	c, _ := s.AddComment("plan.md", 1, 1, "", "first", "", "Ondra", "")

	s.SetCommentAgentStatus("plan.md", c.ID, AgentStateQueued, 0)

	got, _, _ := s.FindCommentByID(c.ID, "plan.md")
	if got.AgentStatus == nil || got.AgentStatus.DeadlineAt != "" {
		t.Errorf("AgentStatus = %+v, want queued without deadline", got.AgentStatus)
	}
}

func TestSetCommentAgentStatus_ReviewLevelComment(t *testing.T) {
	s := newTestSession(t)
	c := s.AddReviewComment("general", "Ondra", "")

	if !s.SetCommentAgentStatus("", c.ID, AgentStateWorking, 45*time.Minute) {
		t.Fatal("SetCommentAgentStatus returned false for a review-level comment")
	}

	got, _ := s.FindReviewCommentByID(c.ID)
	if got.AgentStatus == nil || got.AgentStatus.State != AgentStateWorking {
		t.Errorf("AgentStatus = %+v, want working", got.AgentStatus)
	}
}

func TestSetCommentAgentStatus_UnknownComment(t *testing.T) {
	s := newTestSession(t)

	if s.SetCommentAgentStatus("plan.md", "c_missing", AgentStateDelivered, 0) {
		t.Error("SetCommentAgentStatus returned true for an unknown comment")
	}
}

func TestMergeFromDisk_AdoptsNewerAgentStatus(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	s := newTestSession(t)
	mem := []Comment{{ID: "c1", AgentStatus: agentStatusAt(AgentStateDelivered, t0, DefaultAgentTimeout)}}

	newer := Comment{ID: "c1", AgentStatus: agentStatusAt(AgentStateSeen, t0.Add(time.Second), DefaultAgentTimeout)}
	if !s.mergeCommentRepliesAndState(mem, newer) {
		t.Error("merge reported no change for a newer disk status")
	}
	if mem[0].AgentStatus.State != AgentStateSeen {
		t.Errorf("after newer disk status: state = %q, want %q", mem[0].AgentStatus.State, AgentStateSeen)
	}

	older := Comment{ID: "c1", AgentStatus: agentStatusAt(AgentStateQueued, t0.Add(-time.Second), 0)}
	s.mergeCommentRepliesAndState(mem, older)
	if mem[0].AgentStatus.State != AgentStateSeen {
		t.Errorf("after older disk status: state = %q, want %q", mem[0].AgentStatus.State, AgentStateSeen)
	}
}

func TestMergeReviewCommentFromDisk_AdoptsNewerAgentStatus(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	s := newTestSession(t)
	c := s.AddReviewComment("general", "Ondra", "")

	s.mu.Lock()
	changed := s.mergeReviewCommentRepliesAndState(Comment{ID: c.ID, AgentStatus: agentStatusAt(AgentStateWorking, t0, DefaultAgentTimeout)})
	s.mu.Unlock()

	got, _ := s.FindReviewCommentByID(c.ID)
	if !changed || got.AgentStatus == nil || got.AgentStatus.State != AgentStateWorking {
		t.Errorf("changed=%v AgentStatus=%+v, want adopted working status", changed, got.AgentStatus)
	}
}
