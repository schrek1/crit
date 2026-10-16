package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// startReviewCycle runs POST /api/review-cycle in the background, the way a
// waiting `crit` client does, and returns the channel its response lands on.
func startReviewCycle(t *testing.T, s *Server, query string) <-chan *httptest.ResponseRecorder {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/api/review-cycle"+query, nil))
		done <- w
	}()
	return done
}

func awaitCycle(t *testing.T, done <-chan *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	select {
	case w := <-done:
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("review-cycle response is not JSON: %v (%s)", err, w.Body.String())
		}
		return resp
	case <-time.After(3 * time.Second):
		t.Fatal("review-cycle did not return in time")
		return nil
	}
}

func assertCycleStillWaiting(t *testing.T, done <-chan *httptest.ResponseRecorder) {
	t.Helper()
	select {
	case w := <-done:
		t.Fatalf("review-cycle returned but should still wait: %s", w.Body.String())
	case <-time.After(300 * time.Millisecond):
	}
}

func sendNow(t *testing.T, s *Server, commentID, filePath string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"comment_id":"` + commentID + `","file_path":"` + filePath + `"}`
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/api/agent/request", strings.NewReader(body)))
	return w
}

func postFinish(s *Server) {
	s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/finish", nil))
}

// markAgentLoopActive simulates an agent that waited on review-cycle and is now
// busy between two `crit` calls.
func markAgentLoopActive(session *Session) {
	session.AgentListenerEnter()
	session.AgentListenerExit()
}

func TestReviewCycle_LiveDeliveryReturnsAgentRequest(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "why line1?", "", "reviewer", "")
	done := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	w := sendNow(t, s, c.ID, "test.md")
	resp := awaitCycle(t, done)

	if w.Code != http.StatusAccepted {
		t.Fatalf("Send now: got %d, want 202: %s", w.Code, w.Body.String())
	}
	if resp["status"] != "agent_request" || resp["event"] != "agent_request" || resp["approved"] != false {
		t.Errorf("status=%v event=%v approved=%v, want agent_request/agent_request/false", resp["status"], resp["event"], resp["approved"])
	}
	comments, _ := resp["comments"].([]any)
	if len(comments) != 1 || comments[0].(map[string]any)["id"] != c.ID {
		t.Errorf("comments = %v, want only %s", resp["comments"], c.ID)
	}
	if prompt, _ := resp["prompt"].(string); !strings.Contains(prompt, "This is not Finish Review") || !strings.Contains(prompt, "why line1?") {
		t.Errorf("prompt = %q, want the live-delivery instructions with the comment", prompt)
	}
	got, _, _ := session.FindCommentByID(c.ID, "test.md")
	if got.AgentStatus == nil || got.AgentStatus.State != "delivered" || !got.Live {
		t.Errorf("comment after delivery: live=%v status=%+v, want live and delivered", got.Live, got.AgentStatus)
	}
}

func TestReviewCycle_QueuedRequestReturnsImmediately(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "queued", "", "reviewer", "")
	markAgentLoopActive(session)

	w := sendNow(t, s, c.ID, "test.md")
	queued, _, _ := session.FindCommentByID(c.ID, "test.md")
	resp := awaitCycle(t, startReviewCycle(t, s, ""))

	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"queued"`) {
		t.Errorf("Send now with a busy agent: %d %s, want 202 queued", w.Code, w.Body.String())
	}
	if queued.AgentStatus == nil || queued.AgentStatus.State != "queued" {
		t.Errorf("status before the agent returns = %+v, want queued", queued.AgentStatus)
	}
	if resp["status"] != "agent_request" {
		t.Errorf("status = %v, want agent_request", resp["status"])
	}
}

func TestReviewCycle_NoRoundCompleteAfterLiveDelivery(t *testing.T) {
	s, session := newTestServer(t)
	// A later round: the agent's first call completes the previous round.
	session.SetAwaitingFirstReview(false)
	first := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)
	c, _ := session.AddComment("test.md", 1, 1, "", "keep me", "", "reviewer", "")
	sendNow(t, s, c.ID, "test.md")
	if resp := awaitCycle(t, first); resp["status"] != "agent_request" {
		t.Fatalf("first call: status = %v, want agent_request", resp["status"])
	}

	// The agent replied and runs `crit` again.
	next := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	// SignalRoundComplete would clear the in-memory comments.
	if got := session.GetComments("test.md"); len(got) != 1 {
		t.Errorf("comments after re-run = %d, want 1 (round must not complete)", len(got))
	}
	postFinish(s)
	awaitCycle(t, next)
}

func TestReviewCycle_RoundCompleteDropsQueuedRequests(t *testing.T) {
	s, session := newTestServer(t)
	// The agent is busy with a Finish batch; a request queued now belongs to
	// the round its next `crit` call completes, and the Finish already carried
	// every unresolved comment.
	session.SetAwaitingFirstReview(false)
	c, _ := session.AddComment("test.md", 1, 1, "", "sent during the Finish batch", "", "reviewer", "")
	markAgentLoopActive(session)
	sendNow(t, s, c.ID, "test.md")

	done := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	assertCycleStillWaiting(t, done)
	if left := session.DrainAgentRequests(); len(left) != 0 {
		t.Errorf("queue after round complete = %+v, want empty", left)
	}
	postFinish(s)
	awaitCycle(t, done)
}

func TestReviewCycle_RoundCompletesAfterFinish(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(false)
	session.AddComment("test.md", 1, 1, "", "fix", "", "reviewer", "")
	first := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)
	postFinish(s)
	awaitCycle(t, first)

	next := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	if got := session.GetComments("test.md"); len(got) != 0 {
		t.Errorf("comments after re-run following Finish = %d, want 0 (round completes as today)", len(got))
	}
	postFinish(s)
	awaitCycle(t, next)
}

func TestFinish_QueuedWhenNoListener(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	session.AddComment("test.md", 1, 1, "", "fix", "", "reviewer", "")

	postFinish(s)
	resp := awaitCycle(t, startReviewCycle(t, s, ""))

	if resp["status"] != "finished" || resp["approved"] != false {
		t.Errorf("status=%v approved=%v, want the kept Finish (finished/false)", resp["status"], resp["approved"])
	}
}

func TestReviewCycle_FinishWinsOverQueuedRequest(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "fix", "", "reviewer", "")
	markAgentLoopActive(session)
	sendNow(t, s, c.ID, "test.md")
	postFinish(s)

	resp := awaitCycle(t, startReviewCycle(t, s, ""))

	if resp["status"] != "finished" {
		t.Errorf("status = %v, want finished", resp["status"])
	}
	if left := session.DrainAgentRequests(); len(left) != 0 {
		t.Errorf("queue after Finish = %+v, want empty", left)
	}
}

func TestReviewCycle_TwoListenersOneDelivery(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "one agent only", "", "reviewer", "")
	first := startReviewCycle(t, s, "")
	second := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 2)

	sendNow(t, s, c.ID, "test.md")

	var delivered, waiting <-chan *httptest.ResponseRecorder
	select {
	case w := <-first:
		delivered, waiting = nil, second
		assertAgentRequest(t, w)
	case w := <-second:
		delivered, waiting = nil, first
		assertAgentRequest(t, w)
	case <-time.After(3 * time.Second):
		t.Fatal("no review-cycle returned the live request")
	}
	_ = delivered
	assertCycleStillWaiting(t, waiting)
	postFinish(s)
	if resp := awaitCycle(t, waiting); resp["status"] != "finished" {
		t.Errorf("second listener got %v, want finished", resp["status"])
	}
}

func assertAgentRequest(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if !strings.Contains(w.Body.String(), `"status":"agent_request"`) {
		t.Errorf("response = %s, want status agent_request", w.Body.String())
	}
}

func TestReviewCycle_SkipsDeletedQueuedComment(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "deleted later", "", "reviewer", "")
	markAgentLoopActive(session)
	sendNow(t, s, c.ID, "test.md")
	session.Lock()
	session.Files[0].Comments = []Comment{}
	session.Unlock()

	done := startReviewCycle(t, s, "")

	assertCycleStillWaiting(t, done)
	postFinish(s)
	if resp := awaitCycle(t, done); resp["status"] != "finished" {
		t.Errorf("status = %v, want finished", resp["status"])
	}
}

func TestFinish_ApproveClearsAgentQueue(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "resolved before Finish", "", "reviewer", "")
	markAgentLoopActive(session)
	sendNow(t, s, c.ID, "test.md")
	session.Lock()
	session.Files[0].Comments[0].Resolved = true
	session.Unlock()
	postFinish(s)

	resp := awaitCycle(t, startReviewCycle(t, s, ""))

	if resp["approved"] != true {
		t.Errorf("approved = %v, want true", resp["approved"])
	}
	if left := session.DrainAgentRequests(); len(left) != 0 {
		t.Errorf("queue after Approve = %+v, want empty", left)
	}
}

func TestAgentRequest_PlanHookListenerIsIgnored(t *testing.T) {
	s, session := newTestServer(t)
	session.Mode = "plan"
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "plan comment", "", "reviewer", "")
	done := startReviewCycle(t, s, "?plan_hook=plan_mode")
	waitForSubscriberCount(t, session, 1)

	w := sendNow(t, s, c.ID, "test.md")

	if w.Code != http.StatusBadRequest {
		t.Errorf("Send now with only a plan hook waiting: %d, want 400", w.Code)
	}
	assertCycleStillWaiting(t, done)
	postFinish(s)
	awaitCycle(t, done)
}

func TestFinish_MarksUnansweredCommentsDelivered(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	open, _ := session.AddComment("test.md", 1, 1, "", "unanswered", "", "reviewer", "")
	answered, _ := session.AddComment("test.md", 2, 2, "", "answered", "", "reviewer", "")
	session.AddReply("test.md", answered.ID, "done", "Claude Code", "")
	done := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	postFinish(s)
	awaitCycle(t, done)

	gotOpen, _, _ := session.FindCommentByID(open.ID, "test.md")
	gotAnswered, _, _ := session.FindCommentByID(answered.ID, "test.md")
	if gotOpen.AgentStatus == nil || gotOpen.AgentStatus.State != "delivered" {
		t.Errorf("unanswered comment status = %+v, want delivered", gotOpen.AgentStatus)
	}
	if gotAnswered.AgentStatus != nil {
		t.Errorf("answered comment status = %+v, want none", gotAnswered.AgentStatus)
	}
}

func TestSession_ReportsAgentLoop(t *testing.T) {
	s, session := newTestServer(t)
	done := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/api/session", nil))

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["agent_listening"] != true || resp["agent_loop_active"] != true {
		t.Errorf("agent_listening=%v agent_loop_active=%v, want true/true", resp["agent_listening"], resp["agent_loop_active"])
	}
	postFinish(s)
	awaitCycle(t, done)
}
