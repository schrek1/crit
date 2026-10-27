import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import * as fs from 'fs';
import * as path from 'path';
import { addComment, clearAllComments, loadPage, mdSection, openDocLineComment, roundComplete, sessionCwd } from './helpers';

// ============================================================
// Send to a waiting agent — File Mode (plan and document reviews).
// The files-mode fixture has no CLI state file, so the waiting agent is the
// same long-poll the `crit` client makes: POST /api/review-cycle. The CLI
// output itself is covered by the git-mode spec and Go tests.
// ============================================================

type Cycle = { result: Promise<Record<string, unknown>>; abort: () => void };

async function getTestFilePath(request: APIRequestContext): Promise<string> {
  const session = await (await request.get('/api/session')).json();
  const files = session.files as Array<{ path: string; status: string }>;
  return (files.find(f => f.path.endsWith('.md')) || files[0]).path;
}

async function startWaitingAgent(request: APIRequestContext, baseURL: string): Promise<Cycle> {
  const controller = new AbortController();
  let settled = false;
  const result = fetch(baseURL + '/api/review-cycle', { method: 'POST', signal: controller.signal })
    .then(r => r.json())
    // A cycle still waiting at test end is aborted by afterEach.
    .catch((err) => ({ status: 'aborted', error: String(err) }))
    .finally(() => { settled = true; });
  await expect.poll(async () =>
    settled || (await request.get('/api/session').then(r => r.json())).agent_listening
  ).toBe(true);
  return { result, abort: () => controller.abort() };
}

async function sendNow(request: APIRequestContext, commentId: string, filePath: string) {
  const res = await request.post('/api/agent/request', { data: { comment_id: commentId, file_path: filePath } });
  expect(res.status()).toBe(202);
}

function statusChip(page: Page, commentId: string) {
  return page.locator('.comment-card[data-comment-id="' + commentId + '"] .agent-status');
}

test.describe('Send to a waiting agent — File Mode', () => {
  let cycles: Cycle[] = [];

  test.beforeEach(async ({ request }) => {
    await clearAllComments(request);
    cycles = [];
  });

  test.afterEach(async ({ request }) => {
    cycles.forEach((c) => c.abort());
    await clearAllComments(request);
  });

  test('Send now reaches the waiting agent and its reply shows as Replied', async ({ page, request, baseURL }) => {
    const path = await getTestFilePath(request);
    const cycle = await startWaitingAgent(request, baseURL!);
    cycles.push(cycle);
    const comment = await addComment(request, path, 1, 'Is this step needed?');
    await loadPage(page);

    await sendNow(request, comment.id, path);
    const resp = await cycle.result;

    expect(resp.status).toBe('agent_request');
    expect(resp.approved).toBe(false);
    expect(String(resp.prompt)).toContain('Is this step needed?');
    await expect(statusChip(page, comment.id)).toHaveText('Delivered');

    await request.post(`/api/comment/${comment.id}/replies?path=${encodeURIComponent(path)}`, {
      data: { body: 'Yes, it creates the table.', author: 'Claude Code' },
    });
    await expect(statusChip(page, comment.id)).toHaveText('Replied');
  });

  test('the agent coming back after a live delivery keeps the round', async ({ request, baseURL }) => {
    const path = await getTestFilePath(request);
    const first = await startWaitingAgent(request, baseURL!);
    cycles.push(first);
    const comment = await addComment(request, path, 1, 'live question');
    await sendNow(request, comment.id, path);
    await first.result;
    const roundBefore = (await request.get('/api/session').then(r => r.json())).review_round;

    cycles.push(await startWaitingAgent(request, baseURL!));
    await new Promise((r) => setTimeout(r, 1500));

    const session = await request.get('/api/session').then(r => r.json());
    expect(session.review_round).toBe(roundBefore);
    const comments = await request.get(`/api/file/comments?path=${encodeURIComponent(path)}`).then(r => r.json());
    expect(comments.map((c: { id: string }) => c.id)).toContain(comment.id);
  });

  test('Send now during a Finish batch reaches the agent after the round completes', async ({ request, baseURL }) => {
    const path = await getTestFilePath(request);
    const first = await startWaitingAgent(request, baseURL!);
    cycles.push(first);
    await addComment(request, path, 1, 'finish batch comment');
    await request.post('/api/finish');
    expect((await first.result).status).toBe('finished');

    const late = await addComment(request, path, 3, 'sent during the batch');
    await sendNow(request, late.id, path);
    await new Promise((r) => setTimeout(r, 500)); // let the review file catch up

    const next = await startWaitingAgent(request, baseURL!);
    cycles.push(next);
    const resp = await next.result;

    expect(resp.status).toBe('agent_request');
    expect(String(resp.prompt)).toContain('sent during the batch');
  });
});

// Files changed mid-round (e.g. the agent editing them after Send now) show
// only in the next round; the toast lets the reviewer start it.
test.describe('Files changed mid-round — File Mode', () => {
  let planPath: string;
  let originalPlan: string;
  test.beforeAll(async ({ request }) => {
    planPath = path.join(await sessionCwd(request), 'plan.md');
    originalPlan = fs.readFileSync(planPath, 'utf-8');
  });
  test.afterAll(async ({ request }) => {
    fs.writeFileSync(planPath, originalPlan);
    await roundComplete(request);
  });

  test('the changed-files toast starts the next round', async ({ page }) => {
    await loadPage(page);
    const marker = 'Edited mid-round ' + Date.now();

    fs.writeFileSync(planPath, `${originalPlan}\n\n${marker}\n`);
    const toast = page.locator('.toast-files-changed');
    await expect(toast).toBeVisible({ timeout: 10000 });

    // A second edit keeps the one bar: count the bars at every DOM change.
    await page.evaluate(() => {
      const w = window as unknown as { maxFilesChangedBars: number };
      w.maxFilesChangedBars = 1;
      new MutationObserver(() => {
        const bars = document.querySelectorAll('.toast-files-changed').length;
        w.maxFilesChangedBars = Math.max(w.maxFilesChangedBars, bars);
      }).observe(document.body, { childList: true, subtree: true });
    });
    fs.writeFileSync(planPath, `${originalPlan}\n\n${marker}\n\nSecond edit.\n`);
    await page.waitForTimeout(2500);
    expect(await page.evaluate(() => (window as unknown as { maxFilesChangedBars: number }).maxFilesChangedBars)).toBe(1);
    await toast.getByRole('button', { name: 'Show changes' }).click();

    await expect(page.locator('body')).toContainText(marker);
    await expect(toast).toHaveCount(0);
  });

  test('Shift+R starts the next round while the bar is shown', async ({ page }) => {
    await loadPage(page);
    const marker = 'Shortcut round ' + Date.now();

    fs.writeFileSync(planPath, `${originalPlan}\n\n${marker}\n`);
    await expect(page.locator('.toast-files-changed')).toBeVisible({ timeout: 10000 });
    await page.locator('body').press('Shift+R');

    await expect(page.locator('body')).toContainText(marker);
    await expect(page.locator('.toast-files-changed')).toHaveCount(0);
  });

  // A new round closes every open form, so text not yet saved would be lost.
  test.describe('with a comment or reply being written', () => {
    const DISCARD_QUESTION = 'Discard unsaved comment and show changes?';
    const REPLY_TEXT = 'Half-written reply to shop-checkout';
    const NEW_COMMENT_TEXT = 'Half-written comment on shop-cart';

    test.beforeEach(async ({ request }) => {
      await clearAllComments(request);
      await addComment(request, 'plan.md', 1, 'Is the shop-checkout step still needed?');
    });

    test.afterEach(async ({ request }) => {
      await clearAllComments(request);
    });

    test('Show changes asks first and Cancel keeps the reply and the bar', async ({ page }) => {
      // GIVEN - a reply is being written and the agent changed the file
      await loadPage(page);
      const reply = await startWritingReply(page);
      const marker = 'Kept reply round ' + Date.now();
      fs.writeFileSync(planPath, `${originalPlan}\n\n${marker}\n`);
      const toast = page.locator('.toast-files-changed');
      await expect(toast).toBeVisible({ timeout: 10000 });

      // WHEN - the reviewer clicks Show changes and cancels the question
      const question = answerNextDialog(page, 'dismiss');
      await toast.getByRole('button', { name: 'Show changes' }).click();

      // THEN - the question was asked and nothing changed: same round, reply text and bar kept
      expect(await question).toBe(DISCARD_QUESTION);
      await expect(reply).toHaveValue(REPLY_TEXT);
      await expect(toast).toBeVisible();
      await expect(page.locator('body')).not.toContainText(marker);
    });

    test('Shift+R asks first and OK starts the next round without the new comment', async ({ page }) => {
      // GIVEN - a new comment is being written and the agent changed the file
      await loadPage(page);
      const newComment = await openDocLineComment(page, await mdSection(page), 3);
      await newComment.fill(NEW_COMMENT_TEXT);
      await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
      const marker = 'Discarded comment round ' + Date.now();
      fs.writeFileSync(planPath, `${originalPlan}\n\n${marker}\n`);
      const toast = page.locator('.toast-files-changed');
      await expect(toast).toBeVisible({ timeout: 10000 });

      // WHEN - the reviewer presses Shift+R and confirms the question
      const question = answerNextDialog(page, 'accept');
      await page.locator('body').press('Shift+R');

      // THEN - the next round shows the changed file and the unsaved comment is gone
      expect(await question).toBe(DISCARD_QUESTION);
      await expect(page.locator('body')).toContainText(marker);
      await expect(toast).toHaveCount(0);
      await expect(page.locator('.comment-form textarea')).toHaveCount(0);
    });

    async function startWritingReply(page: Page) {
      const card = page.locator('.comment-card').first();
      await card.locator('.reply-input').click();
      const reply = card.locator('.reply-textarea');
      await reply.fill(REPLY_TEXT);
      return reply;
    }

    // Resolves with the dialog's text; rejects with a "waiting for event dialog" timeout when none comes.
    async function answerNextDialog(page: Page, answer: 'accept' | 'dismiss'): Promise<string> {
      const dialog = await page.waitForEvent('dialog', { timeout: 5000 });
      const message = dialog.message();
      await (answer === 'accept' ? dialog.accept() : dialog.dismiss());
      return message;
    }
  });
});
