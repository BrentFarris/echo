export { copyText } from '../code/ui';

// Terminal feedback must be styled before the lazy Monaco app has loaded.
export function toast(message: string): void {
  let container = document.querySelector<HTMLElement>('.terminal-toasts');
  if (!container) {
    container = document.createElement('div');
    container.className = 'terminal-toasts';
    container.setAttribute('aria-live', 'polite');
    document.body.append(container);
  }
  const item = document.createElement('div'); item.className = 'terminal-toast';
  const text = document.createElement('span'); text.textContent = message;
  const close = document.createElement('button'); close.type = 'button'; close.textContent = '×';
  close.setAttribute('aria-label', 'Dismiss terminal message');
  close.onclick = () => item.remove();
  item.append(text, close); container.append(item);
  window.setTimeout(() => item.remove(), 7000);
}
