import { expect, test } from 'vitest';
import { isQuestionMessage, questionCard, questionResolution, questionSet } from './planQuestions';
import type { Message } from './types';

const settle = () => new Promise(resolve => setTimeout(resolve));

const message = (overrides: Partial<Message>): Message => ({
  id: 'q1', chatId: 'chat', author: 'Kaiju · ask_user_questions', role: 'tool', content: '', status: 'running', createdAt: '', ...overrides,
});

const args = JSON.stringify({
  questions: [
    { id: 'scope', question: 'Which scope?', options: ['Core', 'Extended'] },
    { id: 'lang', question: 'Which language?' },
  ],
});

test('detects question messages and parses the pending set', () => {
  const m = message({ content: args });
  expect(isQuestionMessage(m)).toBe(true);
  expect(isQuestionMessage(message({ author: 'Kaiju · file_read' }))).toBe(false);
  expect(isQuestionMessage(message({ role: 'assistant' }))).toBe(false);
  expect(questionSet(m)).toEqual([
    { id: 'scope', question: 'Which scope?', options: ['Core', 'Extended'] },
    { id: 'lang', question: 'Which language?', options: [] },
  ]);
  expect(questionSet(message({ content: 'not json' }))).toEqual([]);
});

test('pending card renders an open form that submits answers', async () => {
  const submissions: unknown[] = [];
  const card = questionCard(message({ content: args }), async payload => { submissions.push(payload); });
  expect(card).toBeInstanceOf(HTMLDetailsElement);
  expect(card.open).toBe(true);
  expect(card.classList.contains('is-awaiting')).toBe(true);
  expect(card.textContent).toContain('Waiting for your answers…');
  const radios = [...card.querySelectorAll<HTMLInputElement>('input[type="radio"]')];
  expect(radios).toHaveLength(2);
  radios[1].checked = true;
  radios[1].dispatchEvent(new Event('change', { bubbles: true }));
  const custom = card.querySelectorAll<HTMLInputElement>('.question-custom')[1];
  custom.value = 'Go';
  custom.dispatchEvent(new Event('input', { bubbles: true }));
  (card.querySelector('form') as HTMLFormElement).requestSubmit();
  await settle();
  expect(submissions).toEqual([{ answers: [{ questionId: 'scope', optionIndex: 1 }, { questionId: 'lang', optionIndex: -1, text: 'Go' }] }]);
});

test('incomplete answers are blocked with an inline error', async () => {
  let submitted = 0;
  const card = questionCard(message({ content: args }), async () => { submitted++; });
  (card.querySelector('form') as HTMLFormElement).requestSubmit();
  await settle();
  const error = card.querySelector('.question-error') as HTMLElement;
  expect(error.hidden).toBe(false);
  expect(error.textContent).toContain('Answer each question');
  expect(submitted).toBe(0);
  // Typing clears the invalid state.
  const custom = card.querySelector<HTMLInputElement>('.question-custom')!;
  custom.value = 'Core';
  custom.dispatchEvent(new Event('input', { bubbles: true }));
  expect((card.querySelector('.question-field') as HTMLFieldSetElement).classList.contains('is-invalid')).toBe(false);
});

test('skip submits the skip action', async () => {
  const submissions: unknown[] = [];
  const card = questionCard(message({ content: args }), async payload => { submissions.push(payload); });
  [...card.querySelectorAll<HTMLButtonElement>('button')].find(button => button.textContent === 'Skip')!.click();
  await settle();
  expect(submissions).toEqual([{ skipped: true }]);
});

test('completed messages collapse into answered or skipped records', () => {
  const questions = JSON.parse(args).questions;
  const result = JSON.stringify({ answers: [{ questionId: 'scope', optionIndex: 1 }, { questionId: 'lang', optionIndex: -1, text: 'Go' }], skipped: false, questions });
  const m = message({ content: result, status: 'completed' });
  const card = questionCard(m, async () => {});
  expect(card.open).toBe(false);
  expect(card.textContent).toContain('Answered');
  expect(card.textContent).toContain('Extended');
  expect(card.textContent).toContain('Go');
  expect(questionResolution(m)).toEqual({ answers: [{ questionId: 'scope', optionIndex: 1 }, { questionId: 'lang', optionIndex: -1, text: 'Go' }], skipped: false });
  const skipped = questionCard(message({ content: JSON.stringify({ answers: [], skipped: true, questions }), status: 'completed' }), async () => {});
  expect(skipped.textContent).toContain('Skipped');
  expect(skipped.textContent).toContain('Questions skipped.');
});

test('failed and cancelled questions show the reason', () => {
  const card = questionCard(message({ content: JSON.stringify({ error: 'the clarifying questions were canceled before an answer was provided' }), status: 'cancelled' }), async () => {});
  expect(card.open).toBe(false);
  expect(card.textContent).toContain('Cancelled');
  expect(card.textContent).toContain('canceled before an answer');
});
