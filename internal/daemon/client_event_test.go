package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// fakeReviewCycleDaemon serves /api/session as ready and answers
// /api/review-cycle with cycleResp, like a daemon returning one round.
func fakeReviewCycleDaemon(t *testing.T, cycleResp map[string]any) SessionEntry {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/session":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "/api/review-cycle":
			_ = json.NewEncoder(w).Encode(cycleResp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	port := 0
	fmt.Sscanf(ts.URL, "http://127.0.0.1:%d", &port)
	return SessionEntry{Port: port}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestRunReviewClient_PrintsEventLine(t *testing.T) {
	entry := fakeReviewCycleDaemon(t, map[string]any{
		"status":   "agent_request",
		"event":    "agent_request",
		"approved": false,
		"prompt":   "The reviewer sent you 1 comment during the review.",
	})

	var approved bool
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() { approved = RunReviewClient(entry, "", true) })
	})

	if approved {
		t.Error("a live delivery must not report approved")
	}
	if stderr != "approved: false\nevent: agent_request\n" {
		t.Errorf("stderr = %q, want the approved line then the event line", stderr)
	}
	if stdout != "The reviewer sent you 1 comment during the review." {
		t.Errorf("stdout = %q, want the prompt only", stdout)
	}
}

func TestRunReviewClient_FinishHasNoEventLine(t *testing.T) {
	entry := fakeReviewCycleDaemon(t, map[string]any{
		"status":   "finished",
		"approved": false,
		"prompt":   "The review finished with 1 unresolved comment.",
	})

	stderr := captureStderr(t, func() {
		captureStdout(t, func() { RunReviewClient(entry, "", true) })
	})

	if stderr != "approved: false\n" {
		t.Errorf("stderr = %q, want only the approved line", stderr)
	}
}
