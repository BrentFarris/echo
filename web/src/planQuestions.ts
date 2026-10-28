import type { Message } from './types';

// Structured clarifying questions (ported from OLD ask_user_questions): a bot
// pauses its run until the owner answers through the questions endpoint. The
// pending tool message carries the question set in its content; the completed
// message carries the result {answers, skipped, questions}.
export type PlanQuestion = { id: string; question: string; options: string[] };
export type PlanAnswer = { questionId: string; optionIndex: number; text?: string };
export type QuestionSubmission = { answers?: PlanAnswer[]; skipped?: boolean };

export const askUserQuestionsTool = 'ask_user_questions';
const maxAnswerLength = 2000;

export function isQuestionMessage(m: Message): boolean {
  return m.role === 'tool' && m.author.split(' · ').pop() === askUserQuestionsTool;
}

type QuestionPayload = { questions?: unknown; answers?: unknown; skipped?: unknown; error?: unknown };

function parsePayload(content: string): QuestionPayload | null {
  try {
    const value = JSON.parse(content);
    return value && typeof value === 'object' ? value as QuestionPayload : null;
  } catch { return null; }
}

// The question set is available in both states: while running the content is
// the tool arguments, and once completed the result echoes it back.
export function questionSet(m: Message): PlanQuestion[] {
  const payload = parsePayload(m.content);
  const raw = Array.isArray(payload?.questions) ? payload!.questions as Record<string, unknown>[] : [];
  return raw.map(question => ({
    id: String(question?.id || '').trim(),
    question: String(question?.question || '').trim(),
    options: Array.isArray(question?.options) ? (question!.options as unknown[]).map(option => String(option || '').trim()).filter(Boolean) : [],
  })).filter(question => question.id && question.question);
}

export function questionResolution(m: Message): { answers: PlanAnswer[]; skipped: boolean } {
  const payload = parsePayload(m.content);
  return {
    answers: Array.isArray(payload?.answers) ? payload!.answers as PlanAnswer[] : [],
    skipped: Boolean(payload?.skipped),
  };
}

function failureText(m: Message): string {
  const payload = parsePayload(m.content);
  const error = payload?.error;
  return typeof error === 'string' && error ? error : 'The questions could not be completed.';
}

// questionCard renders the interactive modal for a pending set, or the
// collapsed answer record once resolved. The submit callback reports network
// failures through the returned promise so the card can show them inline.
export function questionCard(m: Message, submit: (payload: QuestionSubmission) => Promise<void>): HTMLDetailsElement {
  const questions = questionSet(m);
  const details = document.createElement('details');
  details.className = 'question-card';
  const pending = m.status === 'running';
  details.open = pending;
  if (pending) details.classList.add('is-awaiting');
  const summary = document.createElement('summary');
  const label = document.createElement('span');
  label.textContent = questions.length === 1 ? 'Clarifying question' : 'Clarifying questions';
  const status = document.createElement('span');
  status.className = 'badge';
  summary.append(label, status);
  details.append(summary);
  const body = document.createElement('div');
  body.className = 'question-body';
  details.append(body);

  if (pending && questions.length) {
    status.textContent = 'Waiting for your answers…';
    buildForm(body, questions, submit);
    return details;
  }
  if (m.status !== 'completed' || !questions.length) {
    status.textContent = m.status === 'cancelled' || m.status === 'interrupted' ? 'Cancelled' : 'Failed';
    const failure = document.createElement('p');
    failure.className = 'question-error';
    failure.textContent = failureText(m);
    body.append(failure);
    return details;
  }
  const { answers, skipped } = questionResolution(m);
  status.textContent = skipped ? 'Skipped' : 'Answered';
  if (skipped) {
    const note = document.createElement('p');
    note.className = 'question-skipped';
    note.textContent = 'Questions skipped.';
    body.append(note);
    return details;
  }
  const byQuestion = new Map(answers.map(answer => [answer.questionId, answer]));
  for (const question of questions) {
    const answer = byQuestion.get(question.id);
    const row = document.createElement('div');
    row.className = 'question-answer';
    const prompt = document.createElement('div');
    prompt.className = 'question-answer-prompt';
    prompt.textContent = question.question;
    const value = document.createElement('div');
    value.className = 'question-answer-value';
    value.textContent = answer?.text || question.options[Number(answer?.optionIndex)] || 'No answer';
    row.append(prompt, value);
    body.append(row);
  }
  return details;
}

function buildForm(body: HTMLElement, questions: PlanQuestion[], submit: (payload: QuestionSubmission) => Promise<void>) {
  const form = document.createElement('form');
  form.className = 'question-form';
  const fields: { question: PlanQuestion; fieldset: HTMLFieldSetElement; radios: HTMLInputElement[]; custom: HTMLInputElement }[] = [];
  questions.forEach((question, index) => {
    const fieldset = document.createElement('fieldset');
    fieldset.className = 'question-field';
    const legend = document.createElement('legend');
    legend.textContent = question.question;
    fieldset.append(legend);
    const radios: HTMLInputElement[] = [];
    question.options.forEach((option, optionIndex) => {
      const label = document.createElement('label');
      label.className = 'question-option';
      const radio = document.createElement('input');
      radio.type = 'radio';
      radio.name = `question-${index}`;
      radio.value = String(optionIndex);
      const text = document.createElement('span');
      text.textContent = option;
      label.append(radio, text);
      fieldset.append(label);
      radios.push(radio);
    });
    const custom = document.createElement('input');
    custom.type = 'text';
    custom.className = 'question-custom';
    custom.maxLength = maxAnswerLength;
    custom.placeholder = question.options.length ? 'Or type another answer…' : 'Type your answer…';
    custom.setAttribute('aria-label', `${question.question} — custom answer`);
    custom.addEventListener('input', () => {
      if (custom.value.trim()) radios.forEach(radio => { radio.checked = false; });
      fieldset.classList.remove('is-invalid');
      error.hidden = true;
    });
    radios.forEach(radio => radio.addEventListener('change', () => {
      if (radio.checked) custom.value = '';
      fieldset.classList.remove('is-invalid');
      error.hidden = true;
    }));
    fieldset.append(custom);
    form.append(fieldset);
    fields.push({ question, fieldset, radios, custom });
  });

  const error = document.createElement('p');
  error.className = 'question-error';
  error.hidden = true;
  const actions = document.createElement('div');
  actions.className = 'question-actions';
  const skip = document.createElement('button');
  skip.type = 'button';
  skip.className = 'secondary';
  skip.textContent = 'Skip';
  const send = document.createElement('button');
  send.type = 'submit';
  send.className = 'primary';
  send.textContent = 'Submit answers';
  actions.append(skip, send);
  form.append(error, actions);
  body.append(form);

  const setBusy = (busy: boolean) => { skip.disabled = send.disabled = busy; };
  const fail = (message: string) => { error.textContent = message; error.hidden = false; };
  form.addEventListener('submit', event => {
    event.preventDefault();
    const answers: PlanAnswer[] = [];
    let firstInvalid: HTMLFieldSetElement | null = null;
    for (const field of fields) {
      const customText = field.custom.value.trim();
      const selected = field.radios.find(radio => radio.checked);
      if (!customText && !selected) {
        field.fieldset.classList.add('is-invalid');
        firstInvalid ||= field.fieldset;
        continue;
      }
      answers.push({
        questionId: field.question.id,
        optionIndex: customText ? -1 : Number(selected!.value),
        ...(customText ? { text: customText } : {}),
      });
    }
    if (firstInvalid) {
      fail('Answer each question, or skip this set.');
      firstInvalid.querySelector('input')?.focus();
      return;
    }
    setBusy(true);
    submit({ answers }).catch(() => fail('Could not send your answers. Please try again.')).finally(() => setBusy(false));
  });
  skip.addEventListener('click', () => {
    error.hidden = true;
    setBusy(true);
    submit({ skipped: true }).catch(() => fail('Could not skip these questions. Please try again.')).finally(() => setBusy(false));
  });
}
