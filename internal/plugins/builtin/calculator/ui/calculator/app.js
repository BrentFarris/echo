const display = document.querySelector('#display');
const history = document.querySelector('#history');
const keys = document.querySelector('#keys');
let expression = '';
let nonce = '';
let pluginId = '';
let viewId = '';
let requestNumber = 0;

function render() {
  display.textContent = expression || '0';
}

function applyTheme(theme = {}) {
  for (const [name, value] of Object.entries(theme)) document.documentElement.style.setProperty(name, value);
}

function bridge(method, params = {}) {
  if (!nonce) return;
  window.parent.postMessage({ type: 'echo-plugin-request', nonce, pluginId, viewId, id: `calculator-${++requestNumber}`, method, params }, '*');
}

function calculate(value) {
  const tokens = value.match(/(?:\d+(?:\.\d*)?|\.\d+)|[()+\-*/]/g) || [];
  if (tokens.join('') !== value.replace(/\s/g, '')) throw new Error('Unsupported expression');
  const output = [];
  const operators = [];
  const precedence = { '+': 1, '-': 1, '*': 2, '/': 2 };
  let previous = 'operator';
  for (let token of tokens) {
    if (/^(?:\d|\.)/.test(token)) {
      output.push(Number(token));
      previous = 'number';
      continue;
    }
    if (token === '(') { operators.push(token); previous = 'operator'; continue; }
    if (token === ')') {
      while (operators.length && operators.at(-1) !== '(') output.push(operators.pop());
      if (operators.pop() !== '(') throw new Error('Unbalanced parentheses');
      previous = 'number';
      continue;
    }
    if (token === '-' && previous === 'operator') output.push(0);
    while (operators.length && precedence[operators.at(-1)] >= precedence[token]) output.push(operators.pop());
    operators.push(token);
    previous = 'operator';
  }
  while (operators.length) {
    const operator = operators.pop();
    if (operator === '(') throw new Error('Unbalanced parentheses');
    output.push(operator);
  }
  const stack = [];
  for (const token of output) {
    if (typeof token === 'number') { stack.push(token); continue; }
    const right = stack.pop(); const left = stack.pop();
    if (left === undefined || right === undefined) throw new Error('Incomplete expression');
    stack.push(token === '+' ? left + right : token === '-' ? left - right : token === '*' ? left * right : left / right);
  }
  if (stack.length !== 1 || !Number.isFinite(stack[0])) throw new Error('Cannot calculate');
  return Number(stack[0].toPrecision(12)).toString();
}

function action(kind, value = '') {
  if (kind === 'clear') { expression = ''; history.textContent = ''; }
  else if (kind === 'backspace') expression = expression.slice(0, -1);
  else if (kind === 'equals') {
    try {
      const previous = expression;
      expression = calculate(expression || '0');
      history.textContent = `${previous || '0'} =`;
      bridge('storage.set', { key: 'last-result', value: expression, scope: 'global' });
    } catch { history.textContent = 'Check the expression'; }
  } else if (expression.length < 80) expression += value;
  render();
}

keys.addEventListener('click', event => {
  const button = event.target.closest('button');
  if (button) action(button.dataset.action || 'append', button.dataset.value || '');
});
window.addEventListener('keydown', event => {
  if (/^[0-9()+\-*/.]$/.test(event.key)) action('append', event.key);
  else if (event.key === 'Enter' || event.key === '=') action('equals');
  else if (event.key === 'Backspace') action('backspace');
  else if (event.key === 'Escape') action('clear');
  else return;
  event.preventDefault();
});
window.addEventListener('message', event => {
  const message = event.data || {};
  if (event.source !== window.parent) return;
  if (message.type === 'echo-plugin-init') {
    nonce = message.nonce;
    pluginId = message.pluginId;
    viewId = message.viewId;
    applyTheme(message.theme);
    bridge('storage.get', { key: 'last-result', scope: 'global' });
    return;
  }
  if (!nonce || message.nonce !== nonce || message.pluginId !== pluginId || message.viewId !== viewId) return;
  if (message.type === 'echo-plugin-theme') {
    applyTheme(message.theme);
  } else if (message.type === 'echo-plugin-response' && message.result?.value && !expression) {
    expression = String(message.result.value);
    render();
  }
});
window.parent.postMessage({ type: 'echo-plugin-ready', protocol: 'echo-ui-bridge-1' }, '*');
render();
