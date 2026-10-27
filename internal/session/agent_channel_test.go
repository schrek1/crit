package session

import (
	"testing"
	"time"
)

func TestEnqueue_DedupesByCommentID(t *testing.T) {
	s := newTestSession(t)
	c, _ := s.AddComment("plan.md", 1, 1, "", "first", "", "Ondra", "")

	s.EnqueueAgentRequest(AgentRequest{CommentID: c.ID, FilePath: "plan.md"})
	s.EnqueueAgentRequest(AgentRequest{CommentID: c.ID, FilePath: "plan.md"})

	got := s.DrainAgentRequests()
	if len(got) != 1 || got[0].CommentID != c.ID {
		t.Errorf("DrainAgentRequests = %+v, want one request for %s", got, c.ID)
	}
}

func TestEnqueue_MarksCommentQueued(t *testing.T) {
	s := newTestSession(t)
	c, _ := s.AddComment("plan.md", 1, 1, "", "first", "", "Ondra", "")

	s.EnqueueAgentRequest(AgentRequest{CommentID: c.ID, FilePath: "plan.md"})

	got, _, _ := s.FindCommentByID(c.ID, "plan.md")
	if got.AgentStatus == nil || got.AgentStatus.State != AgentStateQueued {
		t.Errorf("AgentStatus = %+v, want queued", got.AgentStatus)
	}
}

func TestEnqueue_WakesWaitingListener(t *testing.T) {
	s := newTestSession(t)
	c, _ := s.AddComment("plan.md", 1, 1, "", "first", "", "Ondra", "")
	ch := s.Subscribe()
	defer s.Unsubscribe(ch)

	s.EnqueueAgentRequest(AgentRequest{CommentID: c.ID, FilePath: "plan.md"})

	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == "agent-request" {
				return
			}
		case <-deadline:
			t.Fatal("no agent-request event within 1s")
		}
	}
}

func TestDrain_TakesAllOnce(t *testing.T) {
	s := newTestSession(t)
	a, _ := s.AddComment("plan.md", 1, 1, "", "a", "", "Ondra", "")
	b, _ := s.AddComment("plan.md", 3, 3, "", "b", "", "Ondra", "")
	s.EnqueueAgentRequest(AgentRequest{CommentID: a.ID, FilePath: "plan.md"})
	s.EnqueueAgentRequest(AgentRequest{CommentID: b.ID, FilePath: "plan.md"})

	first := s.DrainAgentRequests()
	second := s.DrainAgentRequests()

	if len(first) != 2 || first[0].CommentID != a.ID || first[1].CommentID != b.ID {
		t.Errorf("first drain = %+v, want [%s %s] in order", first, a.ID, b.ID)
	}
	if len(second) != 0 {
		t.Errorf("second drain = %+v, want empty", second)
	}
}

func TestClearAgentRequests_EmptiesQueue(t *testing.T) {
	s := newTestSession(t)
	c, _ := s.AddComment("plan.md", 1, 1, "", "first", "", "Ondra", "")
	s.EnqueueAgentRequest(AgentRequest{CommentID: c.ID, FilePath: "plan.md"})

	s.ClearAgentRequests()

	if got := s.DrainAgentRequests(); len(got) != 0 {
		t.Errorf("DrainAgentRequests after clear = %+v, want empty", got)
	}
}

func TestAgentListener_CountsEnterAndExit(t *testing.T) {
	s := newTestSession(t)

	s.AgentListenerEnter()
	s.AgentListenerEnter()
	s.AgentListenerExit()
	stillListening := s.HasAgentListener()
	s.AgentListenerExit()

	if !stillListening {
		t.Error("HasAgentListener = false with one listener left")
	}
	if s.HasAgentListener() {
		t.Error("HasAgentListener = true after every listener exited")
	}
}

func TestAgentLoopActive_GraceAfterExit(t *testing.T) {
	s := newTestSession(t)
	if s.AgentLoopActive(time.Now()) {
		t.Fatal("AgentLoopActive = true before any agent listened")
	}

	s.AgentListenerEnter()
	if !s.AgentLoopActive(time.Now()) {
		t.Error("AgentLoopActive = false while an agent listens")
	}
	s.AgentListenerExit()
	exited := time.Now()

	if !s.AgentLoopActive(exited.Add(14 * time.Minute)) {
		t.Error("AgentLoopActive = false 14 min after the agent left")
	}
	if s.AgentLoopActive(exited.Add(16 * time.Minute)) {
		t.Error("AgentLoopActive = true 16 min after the agent left")
	}
}

func TestPendingFinish_TakeOnce(t *testing.T) {
	s := newTestSession(t)

	s.SetPendingFinish(`{"approved":false}`)
	first, ok := s.TakePendingFinish()
	_, okAgain := s.TakePendingFinish()

	if !ok || first != `{"approved":false}` {
		t.Errorf("TakePendingFinish = %q, %v, want the stored finish", first, ok)
	}
	if okAgain {
		t.Error("TakePendingFinish returned a finish twice")
	}
}

func TestTakeLastCycleLive_Clears(t *testing.T) {
	s := newTestSession(t)

	s.MarkLastCycleLive(true)
	first := s.TakeLastCycleLive()
	second := s.TakeLastCycleLive()

	if !first || second {
		t.Errorf("TakeLastCycleLive = %v then %v, want true then false", first, second)
	}
}

func TestClearAllComments_ResetsAgentChannel(t *testing.T) {
	s := newTestSession(t)
	c, _ := s.AddComment("plan.md", 1, 1, "", "first", "", "Ondra", "")
	s.EnqueueAgentRequest(AgentRequest{CommentID: c.ID, FilePath: "plan.md"})
	s.AgentListenerEnter()
	s.SetPendingFinish(`{}`)
	s.MarkLastCycleLive(true)

	s.ClearAllComments()

	if got := s.DrainAgentRequests(); len(got) != 0 {
		t.Errorf("queue after ClearAllComments = %+v, want empty", got)
	}
	if s.HasAgentListener() || s.AgentLoopActive(time.Now()) {
		t.Error("agent loop still active after ClearAllComments")
	}
	if _, ok := s.TakePendingFinish(); ok {
		t.Error("pending finish survived ClearAllComments")
	}
	if s.TakeLastCycleLive() {
		t.Error("last-cycle-live flag survived ClearAllComments")
	}
}
