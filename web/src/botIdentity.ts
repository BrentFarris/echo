import { escapeHTML as esc } from './helpers';
import { splitID } from './instances';

type BotIdentity = { id: string; name: string };

// Include the owner even for local IDs, so a bot keeps its color when viewed
// through another Swarm machine. Names and the order of bots do not affect it.
export function botColor(id: string) {
  const [node, local] = splitID(id);
  let hash = 2166136261;
  for (const char of `${node}:${local}`) hash = Math.imul(hash ^ char.codePointAt(0)!, 16777619) >>> 0;
  return `hsl(${(hash % 3600) / 10} ${60 + ((hash >>> 12) % 20)}% ${78 + ((hash >>> 20) % 7)}%)`;
}

function initials(name: string) {
  const words = name.trim().split(/\s+/);
  return [words[0], ...(words.length > 1 ? [words.at(-1)!] : [])].map(word => Array.from(word)[0] || '').join('').toUpperCase() || '?';
}

export function botAvatarMarkup(bot: BotIdentity) {
  return `<span class="bot-avatar" data-bot-avatar="${esc(bot.id)}" style="--bot-color:${botColor(bot.id)}" title="${esc(bot.name)}" aria-hidden="true">${esc(initials(bot.name))}</span>`;
}

export function threadIdentityMarkup(bots: BotIdentity[], selectedId?: string) {
  const active = bots.find(bot => bot.id === selectedId) || bots[0];
  if (!active) return '';
  const others = bots.filter(bot => bot.id !== active.id);
  const visible = [active, ...others].slice(0, 3);
  const names = [active, ...others].map(bot => bot.name).join(', ');
  return `<span class="brand-avatars">${visible.map(botAvatarMarkup).join('')}${bots.length > visible.length ? `<span class="bot-avatar bot-avatar-overflow" aria-hidden="true">+${bots.length - visible.length}</span>` : ''}</span><span class="brand-copy" title="${esc(names)}"><span class="brand-name">${esc(active.name)}</span>${others.length ? `<span class="brand-peers">with ${esc(others.map(bot => bot.name).join(', '))}</span>` : ''}</span>`;
}
