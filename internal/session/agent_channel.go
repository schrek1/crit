package session

import (
	"sync"
	"time"
)

// AgentRequest is a comment the reviewer sent to the agent that waits on the
// review through `crit` (review-cycle). FilePath "" means a review-level comment.
type AgentRequest struct {
	CommentID string
	FilePath  string
}

// agentChannel holds the in-memory state between the reviewer and the agent
// waiting on review-cycle. It has its own lock so review-cycle never waits on
// s.mu. The zero value is ready to use.
type agentChannel struct {
	mu        sync.Mutex
	queue     []AgentRequest
	listeners int
	// everListened and lastExit keep the loop "active" for DefaultAgentTimeout
	// after the agent leaves review-cycle: between two `crit` calls it is
	// working on a delivery, not gone.
	everListened  bool
	lastExit      time.Time
	pendingFinish *string
	lastCycleLive bool
}

// EnqueueAgentRequest queues a comment for the waiting agent, marks it queued
// and wakes any review-cycle. A comment already in the queue is not added twice.
func (s *Session) EnqueueAgentRequest(r AgentRequest) {
	s.agent.mu.Lock()
	duplicate := false
	for _, q := range s.agent.queue {
		if q.CommentID == r.CommentID {
			duplicate = true
			break
		}
	}
	if !duplicate {
		s.agent.queue = append(s.agent.queue, r)
	}
	s.agent.mu.Unlock()

	s.SetCommentAgentStatus(r.FilePath, r.CommentID, AgentStateQueued, 0)
	s.notify(SSEEvent{Type: "agent-request"})
}

// DrainAgentRequests takes every queued request; a concurrent caller gets none.
func (s *Session) DrainAgentRequests() []AgentRequest {
	s.agent.mu.Lock()
	defer s.agent.mu.Unlock()
	queue := s.agent.queue
	s.agent.queue = nil
	return queue
}

// ClearAgentRequests drops the queue, e.g. when a Finish supersedes it.
func (s *Session) ClearAgentRequests() {
	s.agent.mu.Lock()
	s.agent.queue = nil
	s.agent.mu.Unlock()
}

// AgentListenerEnter records an agent waiting on review-cycle. Plan-hook
// clients must not call it.
func (s *Session) AgentListenerEnter() {
	s.agent.mu.Lock()
	s.agent.listeners++
	s.agent.everListened = true
	s.agent.mu.Unlock()
	s.notify(SSEEvent{Type: "agent-status"})
}

// AgentListenerExit records that a waiting agent left review-cycle.
func (s *Session) AgentListenerExit() {
	s.agent.mu.Lock()
	if s.agent.listeners > 0 {
		s.agent.listeners--
	}
	s.agent.lastExit = time.Now()
	s.agent.mu.Unlock()
	s.notify(SSEEvent{Type: "agent-status"})
}

// HasAgentListener reports whether an agent waits on review-cycle right now.
func (s *Session) HasAgentListener() bool {
	s.agent.mu.Lock()
	defer s.agent.mu.Unlock()
	return s.agent.listeners > 0
}

// AgentLoopActive reports whether an agent loop exists at now: an agent
// listens, or one left less than DefaultAgentTimeout ago.
func (s *Session) AgentLoopActive(now time.Time) bool {
	s.agent.mu.Lock()
	defer s.agent.mu.Unlock()
	if s.agent.listeners > 0 {
		return true
	}
	return s.agent.everListened && now.Sub(s.agent.lastExit) < DefaultAgentTimeout
}

// SetPendingFinish keeps a Finish event that no agent was waiting for, so the
// next review-cycle returns it instead of blocking until the next round.
func (s *Session) SetPendingFinish(content string) {
	s.agent.mu.Lock()
	s.agent.pendingFinish = &content
	s.agent.mu.Unlock()
}

// TakePendingFinish returns and clears the kept Finish event.
func (s *Session) TakePendingFinish() (string, bool) {
	s.agent.mu.Lock()
	defer s.agent.mu.Unlock()
	if s.agent.pendingFinish == nil {
		return "", false
	}
	content := *s.agent.pendingFinish
	s.agent.pendingFinish = nil
	return content, true
}

// MarkLastCycleLive records whether the last review-cycle returned a live
// delivery; the next review-cycle must then not complete the round.
func (s *Session) MarkLastCycleLive(v bool) {
	s.agent.mu.Lock()
	s.agent.lastCycleLive = v
	s.agent.mu.Unlock()
}

// TakeLastCycleLive returns and clears the flag set by MarkLastCycleLive.
func (s *Session) TakeLastCycleLive() bool {
	s.agent.mu.Lock()
	defer s.agent.mu.Unlock()
	v := s.agent.lastCycleLive
	s.agent.lastCycleLive = false
	return v
}

// ResetAgentChannel forgets the queue, listeners and pending Finish.
func (s *Session) ResetAgentChannel() {
	s.agent.mu.Lock()
	s.agent.queue = nil
	s.agent.listeners = 0
	s.agent.everListened = false
	s.agent.lastExit = time.Time{}
	s.agent.pendingFinish = nil
	s.agent.lastCycleLive = false
	s.agent.mu.Unlock()
}
