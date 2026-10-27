package server

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/tomasz-tomczyk/crit/internal/comment"
	"github.com/tomasz-tomczyk/crit/internal/prompt"
	"github.com/tomasz-tomczyk/crit/internal/session"
)

// writeLiveDelivery answers a review-cycle with the queued agent requests.
// Requests whose comment no longer exists are dropped; it returns false and
// writes nothing when none is left, so the caller keeps waiting.
func (s *Server) writeLiveDelivery(w http.ResponseWriter, sess *Session) bool {
	var delivered []comment.ListedComment
	for _, req := range sess.DrainAgentRequests() {
		listed, ok := listedAgentComment(sess, req)
		if !ok {
			continue
		}
		delivered = append(delivered, listed)
	}
	if len(delivered) == 0 {
		return false
	}
	for _, listed := range delivered {
		sess.SetCommentAgentStatus(pathOf(listed), listed.ID, session.AgentStateDelivered, 0)
	}
	flushBeforeAgentActs(sess)
	sess.MarkLastCycleLive(true)
	nextCommand := session.NextRoundCommand(sess)
	writeJSON(w, map[string]any{
		"status":       "agent_request",
		"event":        "agent_request",
		"approved":     false,
		"prompt":       s.renderAgentRequestPrompt(sess, delivered, nextCommand),
		"comments":     delivered,
		"next_command": nextCommand,
	})
	return true
}

func listedAgentComment(sess *Session, req session.AgentRequest) (comment.ListedComment, bool) {
	if req.FilePath == "" {
		c, ok := sess.FindReviewCommentByID(req.CommentID)
		if !ok {
			return comment.ListedComment{}, false
		}
		return comment.ListedComment{Scope: "review", Comment: c}, true
	}
	c, filePath, ok := sess.FindCommentByID(req.CommentID, req.FilePath)
	if !ok {
		return comment.ListedComment{}, false
	}
	scope := "line"
	if c.Scope == "file" {
		scope = "file"
	} else if c.EndLine == 0 {
		c.EndLine = c.StartLine
	}
	return comment.ListedComment{Scope: scope, Path: &filePath, Comment: c}, true
}

func pathOf(listed comment.ListedComment) string {
	if listed.Path == nil {
		return ""
	}
	return *listed.Path
}

// finishEventData is the JSON a Finish puts on the session's finish event.
type finishEventData struct {
	Prompt                string                  `json:"prompt"`
	Approved              bool                    `json:"approved"`
	Stats                 map[string]any          `json:"stats"`
	Comments              []comment.ListedComment `json:"comments"`
	PromptMeta            *prompt.Meta            `json:"prompt_meta"`
	NextCommand           string                  `json:"next_command"`
	CloseOnApproveAfterMs *int                    `json:"close_on_approve_after_ms"`
}

// writeCycleFinish answers a review-cycle with a Finish event's content. A
// Finish supersedes queued live requests. With agent_instant_replies on,
// comments the agent has not answered yet are marked delivered without a
// deadline — agents that never report status work through a Finish batch for
// as long as it takes — and answered threads keep their status.
func (s *Server) writeCycleFinish(w http.ResponseWriter, sess *Session, content string) {
	sess.SetAwaitingFirstReview(false)
	sess.ClearAgentRequests()
	sess.MarkLastCycleLive(false)

	var finishData finishEventData
	json.Unmarshal([]byte(content), &finishData)
	if s.cfg.AgentInstantReplies {
		for _, listed := range finishData.Comments {
			if awaitsAgentReply(listed.Comment) {
				sess.SetCommentAgentStatus(pathOf(listed), listed.ID, session.AgentStateDelivered, session.NoAgentDeadline)
			}
		}
		flushBeforeAgentActs(sess)
	}
	nextCommand := finishData.NextCommand
	if nextCommand == "" {
		nextCommand = session.NextRoundCommand(sess)
	}
	cycleResp := map[string]any{
		"status":       "finished",
		"prompt":       finishData.Prompt,
		"approved":     finishData.Approved,
		"comments":     finishData.Comments,
		"stats":        finishData.Stats,
		"next_command": nextCommand,
		"prompt_meta":  finishData.PromptMeta,
	}
	if finishData.CloseOnApproveAfterMs != nil {
		cycleResp["close_on_approve_after_ms"] = *finishData.CloseOnApproveAfterMs
	}
	writeJSON(w, cycleResp)
}

// flushBeforeAgentActs writes the review file before the agent gets its
// comments. The agent's `crit comment` writes that file at once, and a
// debounced daemon write landing afterwards would overwrite its reply.
func flushBeforeAgentActs(sess *Session) {
	// The agent may have written a reply or status since the watcher's last
	// merge; take it in first so this write keeps it.
	sess.MergeAgentWritesFromDisk()
	if err := sess.SyncWriteFiles(); err != nil {
		log.Printf("agent delivery: writing review file: %v", err)
	}
}

// awaitsAgentReply reports whether the reviewer had the last word in c.
func awaitsAgentReply(c Comment) bool {
	if len(c.Replies) == 0 {
		return true
	}
	return c.Replies[len(c.Replies)-1].Author == c.Author
}
