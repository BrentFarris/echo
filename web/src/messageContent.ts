import DOMPurify from 'dompurify';

export function updateStreamedText(element: HTMLElement, text: string): void {
  const node = element.firstChild;
  if (node instanceof Text && node === element.lastChild && text.startsWith(node.data)) {
    if (text.length > node.length) node.appendData(text.slice(node.length));
  } else if (element.textContent !== text) element.textContent = text;
}

/** Patch sanitized output in place so completed blocks and reading state survive. */
export function patchRenderedContent(content: HTMLElement, html: string): void {
  const fragment = DOMPurify.sanitize(html, {
    RETURN_DOM_FRAGMENT: true,
    FORBID_TAGS: ['img', 'iframe', 'form', 'input', 'button', 'style', 'svg', 'math', 'audio', 'video'],
    FORBID_ATTR: ['style', 'id', 'name'],
  });
  for (const link of fragment.querySelectorAll('a')) {
    link.target = '_blank'; link.rel = 'noopener noreferrer';
  }
  patchChildren(content, fragment);
  content.classList.add('markdown');
}

function patchChildren(target: Node, source: Node): void {
  let old = target.firstChild;
  for (const next of [...source.childNodes]) {
    if (!old) { target.appendChild(next); continue; }
    const following = old.nextSibling;
    if (old.nodeType !== next.nodeType || old.nodeName !== next.nodeName) target.replaceChild(next, old);
    else if (old instanceof Element && next instanceof Element) {
      for (const attribute of [...old.attributes]) {
        if (old instanceof HTMLDetailsElement && attribute.name === 'open') continue;
        if (!next.hasAttribute(attribute.name)) old.removeAttribute(attribute.name);
      }
      for (const attribute of [...next.attributes]) {
        if (old.getAttribute(attribute.name) !== attribute.value) old.setAttribute(attribute.name, attribute.value);
      }
      patchChildren(old, next);
    } else if (old instanceof Text && next instanceof Text && next.data.startsWith(old.data)) {
      if (next.length > old.length) old.appendData(next.data.slice(old.length));
    } else if (old.nodeValue !== next.nodeValue) old.nodeValue = next.nodeValue;
    old = following;
  }
  while (old) { const following = old.nextSibling; target.removeChild(old); old = following; }
}
