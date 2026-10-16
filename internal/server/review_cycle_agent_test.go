package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasz-tomczyk/crit/internal/review"
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

// The agent acts on a delivery at once (crit comment --status / --reply-to
// write the review file). A debounced daemon write landing after that would
// overwrite it, so the delivered status must be on disk before review-cycle
// returns.
func TestReviewCycle_LiveDeliveryPersistsStatusBeforeReturning(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "persist me", "", "reviewer", "")
	done := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	sendNow(t, s, c.ID, "test.md")
	awaitCycle(t, done)

	assertDeliveredOnDisk(t, session, c.ID)
}

func TestReviewCycle_FinishPersistsStatusBeforeReturning(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "persist me", "", "reviewer", "")
	done := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	postFinish(s)
	awaitCycle(t, done)

	assertDeliveredOnDisk(t, session, c.ID)
}

func assertDeliveredOnDisk(t *testing.T, session *Session, commentID string) {
	t.Helper()
	cj, err := review.LoadCritJSON(session.CritJSONPath())
	if err != nil {
		t.Fatalf("loading review file: %v", err)
	}
	for _, c := range cj.Files["test.md"].Comments {
		if c.ID == commentID {
			if c.AgentStatus == nil || c.AgentStatus.State != "delivered" {
				t.Errorf("status on disk = %+v, want delivered before review-cycle returns", c.AgentStatus)
			}
			return
		}
	}
	t.Fatalf("comment %s not in the review file", commentID)
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
	// No watcher runs in unit tests; signal its carry-forward as it would.
	session.Notify(SSEEvent{Type: "file-changed", Content: "session"})
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

func TestReviewCycle_RoundCompleteKeepsQueuedRequests(t *testing.T) {
	s, session := newTestServer(t)
	// The agent is busy with a Finish batch and the reviewer sends a new
	// comment. The agent's next call completes the round, which clears the
	// in-memory comments until the watcher carries them forward — the
	// request must wait for that, not be dropped (no watcher runs here).
	session.SetAwaitingFirstReview(false)
	c, _ := session.AddComment("test.md", 1, 1, "", "sent during the Finish batch", "", "reviewer", "")
	markAgentLoopActive(session)
	sendNow(t, s, c.ID, "test.md")

	done := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	assertCycleStillWaiting(t, done)
	if left := session.DrainAgentRequests(); len(left) != 1 || left[0].CommentID != c.ID {
		t.Errorf("queue after round complete = %+v, want the request kept for after carry-forward", left)
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
	// A Finish batch is the classic loop: older agents never report status, so
	// it must not turn into "agent not responding" while they work.
	if gotOpen.AgentStatus != nil && gotOpen.AgentStatus.DeadlineAt != "" {
		t.Errorf("Finish-delivered status has deadline %q, want none", gotOpen.AgentStatus.DeadlineAt)
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

// In a `crit plan --name` loop the plain `crit comment` form does not reliably
// find the plan's review file, so the live prompt must target the plan.
func TestReviewCycle_LiveDeliveryPromptTargetsPlan(t *testing.T) {
	s, session := newTestServer(t)
	session.Mode = "plan"
	session.PlanDir = filepath.Join(t.TempDir(), "my-plan")
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "plan question", "", "reviewer", "")
	done := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	sendNow(t, s, c.ID, "test.md")
	resp := awaitCycle(t, done)

	prompt, _ := resp["prompt"].(string)
	if !strings.Contains(prompt, "crit comment --plan my-plan --reply-to") || !strings.Contains(prompt, "crit comment --plan my-plan --status seen") {
		t.Errorf("plan-mode prompt = %q, want --plan my-plan on reply and status commands", prompt)
	}
}

// The agent replies from the CLI (review file) and runs crit again within the
// watcher's poll window, while the daemon has its own unwritten change. The
// flush before the next delivery must not overwrite that reply.
func TestReviewCycle_DeliveryKeepsAgentReplyFromDisk(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	answered, _ := session.AddComment("test.md", 1, 1, "", "answered from the CLI", "", "reviewer", "")
	if err := session.SyncWriteFiles(); err != nil {
		t.Fatal(err)
	}
	writeAgentReplyOnDisk(t, session, answered.ID, "rp_cli", "done from the CLI")
	markAgentLoopActive(session)
	next, _ := session.AddComment("test.md", 2, 2, "", "queued next", "", "reviewer", "")
	sendNow(t, s, next.ID, "test.md")

	awaitCycle(t, startReviewCycle(t, s, ""))

	cj, err := review.LoadCritJSON(session.CritJSONPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cj.Files["test.md"].Comments {
		if c.ID == answered.ID {
			if len(c.Replies) != 1 || c.Replies[0].ID != "rp_cli" {
				t.Errorf("replies on disk = %+v, want the agent's CLI reply kept", c.Replies)
			}
			return
		}
	}
	t.Fatalf("comment %s missing from the review file", answered.ID)
}

func writeAgentReplyOnDisk(t *testing.T, session *Session, commentID, replyID, body string) {
	t.Helper()
	cj, err := review.LoadCritJSON(session.CritJSONPath())
	if err != nil {
		t.Fatal(err)
	}
	f := cj.Files["test.md"]
	for i := range f.Comments {
		if f.Comments[i].ID == commentID {
			f.Comments[i].Replies = append(f.Comments[i].Replies, Reply{ID: replyID, Body: body, Author: "Claude Code", CreatedAt: time.Now().UTC().Format(time.RFC3339)})
		}
	}
	cj.Files["test.md"] = f
	if err := review.SaveCritJSON(session.CritJSONPath(), cj); err != nil {
		t.Fatal(err)
	}
}

// Between two crit calls the loop counts as active for a grace period; the
// browser needs its end to stop offering Send now when the agent is gone.
func TestSession_ReportsAgentLoopExpiry(t *testing.T) {
	s, session := newTestServer(t)
	markAgentLoopActive(session)
	left := time.Now()

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/api/session", nil))

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	until, err := time.Parse(time.RFC3339Nano, fmt.Sprint(resp["agent_loop_until"]))
	if err != nil {
		t.Fatalf("agent_loop_until = %v, want a timestamp: %v", resp["agent_loop_until"], err)
	}
	if d := until.Sub(left); d < 14*time.Minute || d > 16*time.Minute {
		t.Errorf("agent_loop_until is %v after the agent left, want ~15m", d)
	}
}
