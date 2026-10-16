'use strict';
// Agent status helpers mirror Go's session.EffectiveAgentState: the stored
// states plus the derived "replied" and "not_responding".
const { test } = require('node:test');
const assert = require('node:assert/strict');
const helpers = require('../crit-comment-card-helpers.js');

const T0 = Date.parse('2026-10-16T10:00:00.000Z');
const MIN = 60 * 1000;

function withStatus(state, updatedMs, timeoutMs, replies) {
  const status = { state: state, updated_at: new Date(updatedMs).toISOString() };
  if (timeoutMs) status.deadline_at = new Date(updatedMs + timeoutMs).toISOString();
  return { id: 'c1', author: 'Ondra', agent_status: status, replies: replies || [] };
}

function reply(author, ms) {
  // Reply timestamps carry whole seconds, like the Go side.
  return { author: author, created_at: new Date(ms).toISOString().replace(/\.\d{3}Z$/, 'Z') };
}

test('effectiveAgentState is empty without a status', () => {
  assert.equal(helpers.effectiveAgentState({ id: 'c1', author: 'Ondra' }, T0), '');
});

test('effectiveAgentState replied after non-author reply', () => {
  const c = withStatus('working', T0, 15 * MIN, [reply('Claude Code', T0 + 1000)]);
  assert.equal(helpers.effectiveAgentState(c, T0 + 2000), 'replied');
});

test('effectiveAgentState replied within the same second', () => {
  const c = withStatus('working', T0 + 700, 15 * MIN, [reply('Claude Code', T0)]);
  assert.equal(helpers.effectiveAgentState(c, T0 + 1000), 'replied');
});

test('effectiveAgentState ignores the reviewer own reply', () => {
  const c = withStatus('working', T0, 15 * MIN, [reply('Ondra', T0 + 1000)]);
  assert.equal(helpers.effectiveAgentState(c, T0 + 2000), 'working');
});

test('effectiveAgentState not_responding after deadline', () => {
  const c = withStatus('delivered', T0, 15 * MIN);
  assert.equal(helpers.effectiveAgentState(c, T0 + 16 * MIN), 'not_responding');
  assert.equal(helpers.effectiveAgentState(c, T0 + 14 * MIN), 'delivered');
});

test('effectiveAgentState queued has no deadline', () => {
  const c = withStatus('queued', T0, 0);
  assert.equal(helpers.effectiveAgentState(c, T0 + 60 * MIN), 'queued');
});

test('agentStateLabel maps every state', () => {
  assert.equal(helpers.agentStateLabel('queued'), 'Queued');
  assert.equal(helpers.agentStateLabel('delivered'), 'Delivered');
  assert.equal(helpers.agentStateLabel('seen'), 'Seen');
  assert.equal(helpers.agentStateLabel('working'), 'Working');
  assert.equal(helpers.agentStateLabel('replied'), 'Replied');
  assert.equal(helpers.agentStateLabel('not_responding'), 'Agent not responding');
});

test('agentStatusDeadlineMs returns the deadline only while the agent owes an answer', () => {
  assert.equal(helpers.agentStatusDeadlineMs(withStatus('working', T0, 15 * MIN)), T0 + 15 * MIN);
  assert.equal(helpers.agentStatusDeadlineMs(withStatus('queued', T0, 0)), 0);
  assert.equal(helpers.agentStatusDeadlineMs({ id: 'c1' }), 0);
});
