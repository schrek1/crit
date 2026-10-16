import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { spawn, execSync } from 'child_process';
import {
  clearAllComments, addComment, loadPage, switchToDocumentView, getMdPath, openDocLineComment, readFixtureState,
} from './helpers';

// ============================================================
// Send to a waiting agent — a real `crit --session <key>` client waits on the
// review the way an agent does, and Send now reaches it mid-round. The fixture
// also has agent_cmd ("echo"); the waiting agent must win over it.
// ============================================================

type AgentRun = {
  done: Promise<{ code: number | null; stdout: string; stderr: string }>;
  kill: () => void;
};

function fixtureEnv() {
  const env = readFixtureState();
  if (!env['CRIT_BIN'] || !env['CRIT_FIXTURE_DIR'] || !env['FAKE_HOME']) {
    throw new Error('CRIT_BIN, CRIT_FIXTURE_DIR, or FAKE_HOME not set in state file');
  }
  return env;
}

// On Windows Go's os.UserHomeDir() reads USERPROFILE, so both point at the
// fixture's home, where the daemon registered its session.
function processEnv(fakeHome: string) {
  return { ...process.env, HOME: fakeHome, USERPROFILE: fakeHome };
}

// startWaitingAgent runs the real CLI client against the fixture daemon and
// resolves once it has registered as a listener — or has already returned,
// which it does at once when a request or a Finish was waiting for it.
async function startWaitingAgent(request: APIRequestContext): Promise<AgentRun> {
  const env = fixtureEnv();
  const session = await request.get('/api/session').then(r => r.json());
  expect(session.session_key).toBeTruthy();
  const child = spawn(env['CRIT_BIN'], ['--session', session.session_key, '--no-open'], {
    cwd: env['CRIT_FIXTURE_DIR'], env: processEnv(env['FAKE_HOME']),
  });
  let stdout = '';
  let stderr = '';
  child.stdout.on('data', (d) => { stdout += d.toString(); });
  child.stderr.on('data', (d) => { stderr += d.toString(); });
  let exited = false;
  const done = new Promise<{ code: number | null; stdout: string; stderr: string }>((resolve) => {
    child.on('close', (code) => { exited = true; resolve({ code, stdout, stderr }); });
  });
  await expect.poll(async () =>
    exited || (await request.get('/api/session').then(r => r.json())).agent_listening
  ).toBe(true);
  return { done, kill: () => child.kill() };
}

async function expectStillWaiting(run: AgentRun, ms = 1500) {
  const result = await Promise.race([run.done, new Promise((r) => setTimeout(() => r('waiting'), ms))]);
  expect(result).toBe('waiting');
}

function critComment(args: string) {
  const env = fixtureEnv();
  execSync(`"${env['CRIT_BIN']}" comment ${args}`, {
    shell: true, timeout: 5000, cwd: env['CRIT_FIXTURE_DIR'], env: processEnv(env['FAKE_HOME']),
  } as const);
}

async function sendNow(request: APIRequestContext, commentId: string, filePath: string) {
  const res = await request.post('/api/agent/request', { data: { comment_id: commentId, file_path: filePath } });
  expect(res.status()).toBe(202);
  return res.json();
}

function statusChip(page: Page, commentId: string) {
  return page.locator('.comment-card[data-comment-id="' + commentId + '"] .agent-status');
}

// deliverOne makes the agent loop active: one live round trip with a comment.
// The agent connects first: in a later round its first call completes the
// previous round, which would drop a comment added just before.
async function deliverOne(request: APIRequestContext, mdPath: string) {
  const run = await startWaitingAgent(request);
  const comment = await addComment(request, mdPath, 3, 'warm-up');
  await sendNow(request, comment.id, mdPath);
  await run.done;
  return comment;
}

test.describe('Send to a waiting agent', () => {
  let runs: AgentRun[] = [];

  test.beforeEach(async ({ request }) => {
    await clearAllComments(request);
    runs = [];
  });

  test.afterEach(async ({ request }) => {
    runs.forEach((run) => run.kill());
    await clearAllComments(request);
  });

  test('Send now reaches the waiting agent and its reply shows as Replied', async ({ page, request }) => {
    const mdPath = await getMdPath(request);
    const run = await startWaitingAgent(request);
    runs.push(run);
    const comment = await addComment(request, mdPath, 1, 'Why this heading?');
    await loadPage(page);
    await switchToDocumentView(page);

    const sent = await sendNow(request, comment.id, mdPath);
    const result = await run.done;

    expect(sent.status).toBe('queued');
    expect(result.code).toBe(0);
    expect(result.stderr).toContain('approved: false');
    expect(result.stderr).toContain('event: agent_request');
    expect(result.stdout).toContain('Why this heading?');
    await expect(statusChip(page, comment.id)).toHaveText('Delivered');

    critComment(`--reply-to ${comment.id} --author "Claude Code" "Because it names the plan."`);
    await expect(statusChip(page, comment.id)).toHaveText('Replied');
  });

  test('running crit again after a live delivery keeps the round and open drafts', async ({ page, request }) => {
    const mdPath = await getMdPath(request);
    await deliverOne(request, mdPath);
    const roundBefore = (await request.get('/api/session').then(r => r.json())).review_round;
    await loadPage(page);
    const doc = await switchToDocumentView(page);
    const draft = await openDocLineComment(page, doc, 1);
    await draft.fill('a draft the reviewer is still typing');

    runs.push(await startWaitingAgent(request));

    await expectStillWaiting(runs[0]);
    await expect(draft).toHaveValue('a draft the reviewer is still typing');
    const roundAfter = (await request.get('/api/session').then(r => r.json())).review_round;
    expect(roundAfter).toBe(roundBefore);
  });

  test('a reviewer reply in a live thread goes to the agent again', async ({ request }) => {
    const mdPath = await getMdPath(request);
    const comment = await deliverOne(request, mdPath);
    const run = await startWaitingAgent(request);
    runs.push(run);

    await request.post(`/api/comment/${comment.id}/replies?path=${encodeURIComponent(mdPath)}`, { data: { body: 'and one more thing' } });
    await sendNow(request, comment.id, mdPath);
    const result = await run.done;

    expect(result.stderr).toContain('event: agent_request');
    expect(result.stdout).toContain('and one more thing');
  });

  test('a comment sent while the agent is busy is queued and delivered on its next run', async ({ page, request }) => {
    const mdPath = await getMdPath(request);
    await deliverOne(request, mdPath);
    const comment = await addComment(request, mdPath, 1, 'sent while busy');
    await loadPage(page);
    await switchToDocumentView(page);

    await sendNow(request, comment.id, mdPath);
    await expect(statusChip(page, comment.id)).toHaveText('Queued');
    const run = await startWaitingAgent(request);
    runs.push(run);
    const result = await run.done;

    expect(result.stdout).toContain('sent while busy');
    await expect(statusChip(page, comment.id)).toHaveText('Delivered');
    await page.reload();
    await switchToDocumentView(page);
    await expect(statusChip(page, comment.id)).toHaveText('Delivered');
  });

  test('the Send every comment option delivers a plain comment', async ({ page, request }) => {
    await page.context().addCookies([{
      name: 'crit-settings',
      value: encodeURIComponent(JSON.stringify({ diffScope: 'all', autoSendToAgent: true })),
      domain: 'localhost',
      path: '/',
    }]);
    const run = await startWaitingAgent(request);
    runs.push(run);
    await loadPage(page);
    const doc = await switchToDocumentView(page);

    const textarea = await openDocLineComment(page, doc);
    await textarea.fill('auto-sent comment');
    await doc.locator('.comment-form .btn-primary').last().click();
    const result = await run.done;

    expect(result.stderr).toContain('event: agent_request');
    expect(result.stdout).toContain('auto-sent comment');
  });

  test('without the option a plain comment waits for Finish', async ({ page, request }) => {
    const run = await startWaitingAgent(request);
    runs.push(run);
    await loadPage(page);
    const doc = await switchToDocumentView(page);

    const textarea = await openDocLineComment(page, doc);
    await textarea.fill('plain comment');
    await doc.locator('.comment-form .btn-primary').last().click();

    await expectStillWaiting(run, 2000);
  });

  test('a Finish clicked while the agent is busy reaches its next run', async ({ request }) => {
    const mdPath = await getMdPath(request);
    await deliverOne(request, mdPath);

    await request.post('/api/finish');
    const run = await startWaitingAgent(request);
    runs.push(run);
    const result = await run.done;

    expect(result.stderr).toContain('approved: false');
    expect(result.stderr).not.toContain('event:');
    expect(result.stdout).toContain('unresolved comment');
  });

  test('status reports from the agent update the chip', async ({ page, request }) => {
    const mdPath = await getMdPath(request);
    const comment = await deliverOne(request, mdPath);
    await loadPage(page);
    await switchToDocumentView(page);

    critComment(`--status working ${comment.id}`);
    await expect(statusChip(page, comment.id)).toHaveText('Working');

    critComment(`--status working --timeout 1s ${comment.id}`);
    await expect(statusChip(page, comment.id)).toHaveText('Agent not responding', { timeout: 10_000 });
  });
});
