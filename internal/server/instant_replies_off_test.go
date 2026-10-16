package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// With agent_instant_replies off (the default) crit must behave exactly like
// upstream: no delivery to the waiting agent, no kept Finish, no statuses.

func TestInstantRepliesOff_SendNowWithWaitingAgentNeedsAgentCmd(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "no agent_cmd", "", "reviewer", "")
	done := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	w := sendNow(t, s, c.ID, "test.md")

	if w.Code != http.StatusBadRequest {
		t.Errorf("Send now without agent_cmd: got %d, want 400 (upstream)", w.Code)
	}
	assertCycleStillWaiting(t, done)
	postFinish(s)
	awaitCycle(t, done)
}

func TestInstantRepliesOff_SendNowRunsAgentCmd(t *testing.T) {
	s, session := newTestServer(t)
	s.agentCmd = "echo test"
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "for agent_cmd", "", "reviewer", "")
	done := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	w := sendNow(t, s, c.ID, "test.md")

	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"accepted"`) {
		t.Errorf("Send now: %d %s, want 202 accepted (agent_cmd, as upstream)", w.Code, w.Body.String())
	}
	got, _, _ := session.FindCommentByID(c.ID, "test.md")
	if got.AgentStatus != nil {
		t.Errorf("agent_cmd delivery set status %+v, want none", got.AgentStatus)
	}
	assertCycleStillWaiting(t, done)
	postFinish(s)
	awaitCycle(t, done)
}

func TestInstantRepliesOff_FinishWithoutListenerIsNotKept(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	session.AddComment("test.md", 1, 1, "", "fix", "", "reviewer", "")

	postFinish(s)
	done := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	assertCycleStillWaiting(t, done)
	postFinish(s)
	awaitCycle(t, done)
}

func TestInstantRepliesOff_FinishSetsNoStatus(t *testing.T) {
	s, session := newTestServer(t)
	session.SetAwaitingFirstReview(true)
	c, _ := session.AddComment("test.md", 1, 1, "", "fix", "", "reviewer", "")
	done := startReviewCycle(t, s, "")
	waitForSubscriberCount(t, session, 1)

	postFinish(s)
	awaitCycle(t, done)

	got, _, _ := session.FindCommentByID(c.ID, "test.md")
	if got.AgentStatus != nil {
		t.Errorf("Finish set status %+v, want none", got.AgentStatus)
	}
}

func TestInstantReplies_ConfigReportsTheSwitch(t *testing.T) {
	for _, on := range []bool{false, true} {
		s, _ := newTestServer(t)
		s.cfg.AgentInstantReplies = on

		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", "/api/config", nil))

		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp["agent_instant_replies"] != on {
			t.Errorf("agent_instant_replies = %v, want %v", resp["agent_instant_replies"], on)
		}
	}
}
