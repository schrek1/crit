package session

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasz-tomczyk/crit/internal/vcs"
)

// Files mode: a round's comments are carried onto the next round from the text
// the page showed in that round, onto the text on disk at round-complete.

func TestHandleRoundCompleteFiles_RoundWithoutEdits_KeepsCommentAndShowsNoDiff(t *testing.T) {
	round1 := "# Plan\n- [ ] review\n- [ ] review\nend\n"
	round2 := "# Plan\nintro\n- [ ] review\n- [ ] review\nend\n"
	s := newMarkdownFilesSession(t, map[string]string{"plan.md": round1}, "plan.md")
	events := s.Subscribe()
	defer s.Unsubscribe(events)
	startWatcher(t, s.watchFileMtimes)

	// GIVEN - round 1: the agent inserts a line above the checklist
	writeFileNewMtime(t, s.Files[0].AbsPath, round2)
	waitForSSE(t, events, "edit-detected")
	completeRoundAndWait(t, s, events)

	// AND - round 2: the reviewer comments on the first of two identical items, the agent edits nothing
	if _, ok := s.AddComment("plan.md", 3, 3, "", "first item", "", "tester", ""); !ok {
		t.Fatal("AddComment failed")
	}

	// WHEN - round 2 completes
	completeRoundAndWait(t, s, events)

	// THEN - the comment stays on its item and round 3 shows no changes
	assertCommentOnLine(t, s, "plan.md", round2, 3)
	if hunks, previous := markdownDiff(t, s, "plan.md"); hunks != 0 {
		t.Errorf("round 3 diff has %d hunks against previous_content=%q; round 2 changed nothing", hunks, previous)
	}
}

func TestHandleRoundCompleteFiles_FileEditedInLaterWatcherTick_CarriesCommentAndShowsDiff(t *testing.T) {
	alpha := "# A\n\nalpha\n"
	beta := "# B\n\none\n\ntarget line\n"
	betaEdited := "# B\n\nnew\n\none\n\ntarget line\n"
	s := newMarkdownFilesSession(t, map[string]string{"a.md": alpha, "b.md": beta}, "a.md", "b.md")
	events := s.Subscribe()
	defer s.Unsubscribe(events)
	startWatcher(t, s.watchFileMtimes)

	// GIVEN - the reviewer comments on "target line" in b.md
	if _, ok := s.AddComment("b.md", 5, 5, "", "about target", "", "tester", ""); !ok {
		t.Fatal("AddComment failed")
	}

	// AND - the agent edits a.md first, then b.md after the watcher took in a.md
	writeFileNewMtime(t, s.Files[0].AbsPath, alpha+"\nmore\n")
	waitForSSE(t, events, "edit-detected")
	writeFileNewMtime(t, s.Files[1].AbsPath, betaEdited)
	waitForSSE(t, events, "edit-detected")

	// WHEN - the round completes
	completeRoundAndWait(t, s, events)

	// THEN - the b.md comment follows its line and b.md shows its change
	assertCommentOnLine(t, s, "b.md", betaEdited, 7)
	if hunks, previous := markdownDiff(t, s, "b.md"); hunks == 0 || previous != beta {
		t.Errorf("b.md diff: %d hunks against previous_content=%q; want the round-start text", hunks, previous)
	}
}

func TestHandleRoundCompleteFiles_EditNotYetPolled_UsesTextOnDisk(t *testing.T) {
	tests := []struct {
		name          string
		earlierRounds []string // texts written in rounds completed before, each seen by the watcher
		roundStart    string
		edited        string
	}{
		{
			name:       "first round",
			roundStart: "# Plan\n\ntarget line\n",
			edited:     "# Plan\n\nnew one\nnew two\n\ntarget line\n",
		},
		{
			name:          "later round",
			earlierRounds: []string{"# Plan\n\ntarget line\n\nsecond\n"},
			roundStart:    "# Plan\n\ntarget line\n\nsecond\n",
			edited:        "# Plan\n\nnew one\nnew two\n\ntarget line\n\nsecond\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newMarkdownFilesSession(t, map[string]string{"plan.md": "# Plan\n\ntarget line\n"}, "plan.md")
			events := s.Subscribe()
			defer s.Unsubscribe(events)

			// GIVEN - earlier rounds completed with every edit seen by the watcher
			stopWatcher := startWatcher(t, s.watchFileMtimes)
			for _, text := range tt.earlierRounds {
				writeFileNewMtime(t, s.Files[0].AbsPath, text)
				waitForSSE(t, events, "edit-detected")
				completeRoundAndWait(t, s, events)
			}
			stopWatcher()

			// AND - a watcher that does not poll again before round-complete
			s.pollInterval = time.Hour
			startWatcher(t, s.watchFileMtimes)

			// AND - a comment on "target line", then the agent inserts two lines above it
			if _, ok := s.AddComment("plan.md", 3, 3, "", "about target", "", "tester", ""); !ok {
				t.Fatal("AddComment failed")
			}
			writeFileNewMtime(t, s.Files[0].AbsPath, tt.edited)
			nextRound := s.GetReviewRound() + 1

			// WHEN - round-complete arrives right after the edit
			completeRoundAndWait(t, s, events)

			// THEN - the comment follows its line, the diff and the timeline hold the edited text
			assertCommentOnLine(t, s, "plan.md", tt.edited, 6)
			if _, previous := markdownDiff(t, s, "plan.md"); previous != tt.roundStart {
				t.Errorf("previous_content=%q; want the round-start text %q", previous, tt.roundStart)
			}
			s.mu.RLock()
			timeline := s.RoundSnapshots["plan.md"][nextRound].Content
			s.mu.RUnlock()
			if timeline != tt.edited {
				t.Errorf("round %d timeline snapshot=%q; want the edited text", nextRound, timeline)
			}
		})
	}
}

func newMarkdownFilesSession(t *testing.T, contents map[string]string, order ...string) *Session {
	t.Helper()
	dir := t.TempDir()
	s := &Session{
		Mode:          "files",
		RepoRoot:      dir,
		ReviewRound:   1,
		pollInterval:  testPollInterval,
		subscribers:   make(map[chan SSEEvent]struct{}),
		roundComplete: make(chan struct{}, 1),
	}
	for _, name := range order {
		path := filepath.Join(dir, name)
		writeFile(t, path, contents[name])
		s.Files = append(s.Files, &FileEntry{
			Path:     name,
			AbsPath:  path,
			Status:   "added",
			FileType: "markdown",
			Content:  contents[name],
			FileHash: fileHash([]byte(contents[name])),
			Comments: []Comment{},
		})
	}
	t.Cleanup(func() { quiesceSession(t, s) })
	return s
}

// completeRoundAndWait persists the round's comments, as the debounced write
// does before the reviewer finishes, then completes the round.
func completeRoundAndWait(t *testing.T, s *Session, events chan SSEEvent) {
	t.Helper()
	if err := s.SyncWriteFiles(); err != nil {
		t.Fatal(err)
	}
	s.SignalRoundComplete()
	waitForSSE(t, events, "file-changed")
}

func assertCommentOnLine(t *testing.T, s *Session, path, content string, want int) {
	t.Helper()
	comments := s.GetComments(path)
	if len(comments) != 1 {
		t.Fatalf("%s: %d comments, want 1", path, len(comments))
	}
	if got := comments[0].StartLine; got != want {
		lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
		at := "<out of range>"
		if got >= 1 && got <= len(lines) {
			at = lines[got-1]
		}
		t.Errorf("%s: comment on line %d (%q), want %d (%q)", path, got, at, want, lines[want-1])
	}
}

func markdownDiff(t *testing.T, s *Session, path string) (hunks int, previous string) {
	t.Helper()
	snapshot, ok := s.GetFileDiffSnapshot(path, false)
	if !ok {
		t.Fatalf("%s diff missing", path)
	}
	return len(snapshot["hunks"].([]vcs.DiffHunk)), snapshot["previous_content"].(string)
}
