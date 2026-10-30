import { describe, expect, it, beforeEach } from 'vitest';
import { bindMentions, hashtags, markers, renderNote } from './model';
import { combineState } from '../instances';
import type { Bot, State } from '../types';

const home = 'a'.repeat(32), remote = 'b'.repeat(32);
const state = { bots: [], plugins: [], chats: [], endpoints: [], schedules: [], occurrences: [], runs: [], appDock: [] } as State;
const bots = [
  { id: 'bot1', nodeId: home, handle: 'kaiju' },
  { id: `${remote}:bot2`, nodeId: remote, handle: 'linux.kaiju' },
] as Bot[];
beforeEach(() => combineState(state, { node: { nodeId: home, name: 'desktop', epoch: '', version: 1 }, enabled: true, peers: [], instances: [{ node: { nodeId: remote, name: 'linux', epoch: '', version: 1 }, online: true, state }] }));

describe('Notes Markdown metadata', () => {
  it('extracts tags and mentions from prose with precise source ranges', () => {
    const text = '# Heading\n\n😀 **#Plan** and @kaiju, [#linked](https://example.org/#ignored).\n\n- #list\n\n> #quote\n\n| Item |\n| --- |\n| #table |';
    expect(hashtags(text)).toEqual(['linked', 'list', 'plan', 'quote', 'table']);
    for (const marker of markers(text)) expect(text.slice(marker.start, marker.end)).toBe(marker.text);
  });
  it('ignores code, escapes, raw HTML, headings and URL destinations', () => {
    const text = '# Heading\n\n`#inline @kaiju` \\#escaped \\@kaiju\n\n```md\n#fenced @kaiju\n```\n\n    #indented\n\n<div>#html @kaiju</div>\n\nA <span>#inside @kaiju</span> #outside [url](https://example.org/#destination) https://example.org/#auto';
    expect(hashtags(text)).toEqual(['outside']);
    expect(markers(text).some(m => m.text.startsWith('@'))).toBe(false);
  });
  it('supports Unicode tags and deduplicates case', () => expect(hashtags('#Café #CAFÉ #follow-up #project_1')).toEqual(['café', 'follow-up', 'project_1']));
  it.each(['\n', '\r\n'])('keeps offsets in multiline quotes and nested lists with %j line endings', newline => {
    const text = ['> First #one @kaiju', '> Second #two', '', '- First #three', '  continuation #four', '  - nested #five'].join(newline);
    expect(hashtags(text)).toEqual(['five', 'four', 'one', 'three', 'two']);
    for (const marker of markers(text)) expect(text.slice(marker.start, marker.end)).toBe(marker.text);
  });
  it('binds bare handles relative to the note owner and qualified handles across machines', () => {
    expect(bindMentions('@kaiju @linux.kaiju', home, bots).map(m => m.botId)).toEqual(['bot1', 'bot2']);
    expect(bindMentions('@kaiju @desktop.kaiju', remote, bots).map(m => m.botId)).toEqual(['bot2', 'bot1']);
    expect(bindMentions('@missing', home, bots)).toEqual([]);
  });
  it('retains stable identities across insertions, renames and deletion from inventory', () => {
    const text = 'hello @kaiju end', bindings = bindMentions(text, home, bots);
    const changed = '😀 hello @kaiju end';
    const result = bindMentions(changed, home, [], text, bindings);
    expect(result).toEqual([{ ...bindings[0], start: 9, end: 15 }]);
    expect(bindMentions(changed, home, [{ ...bots[0], handle: 'renamed' }], changed, result)).toEqual(result);
    expect(bindMentions('hello end', home, bots, text, bindings)).toEqual([]);
    expect(bindMentions('hello `@kaiju` end', home, bots, text, bindings)).toEqual([]);
  });
});

it('renders sanitized read-only GFM without workspace paths or host markup', () => {
  const host = document.createElement('div');
  host.append(renderNote('# Title\n\n- [ ] task\n\n[web](https://example.org) [file](../secret) ![local](secret.png)\n\n<script>evil()</script><img src="javascript:evil()" onerror="evil()"><input type="text"><div data-note="bad" style="position:fixed">x</div>'));
  expect(host.querySelector('h1')?.textContent).toBe('Title');
  expect(host.querySelector<HTMLInputElement>('input')?.disabled).toBe(true);
  expect(host.querySelectorAll('input')).toHaveLength(1);
  expect(host.querySelector('script,[onerror],[style],[data-note]')).toBeNull();
  expect(host.querySelectorAll('a[href]')).toHaveLength(1);
  expect(host.querySelector('a')?.rel).toBe('noopener noreferrer');
  expect(host.querySelector('img')).toBeNull();
});
