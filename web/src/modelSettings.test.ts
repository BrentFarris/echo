import { expect, test } from 'vitest';
import { endpointForm, endpointValues } from './modelSettings';
import type { Endpoint } from './types';

function form(endpoint?: Endpoint, cloud = false) {
  const root = document.createElement('div');
  root.innerHTML = endpointForm(endpoint, cloud);
  return root.querySelector('form')!;
}

test('new local and cloud connections have no execution timeouts', () => {
  for (const cloud of [false, true]) {
    const element = form(undefined, cloud);
    for (const name of ['requestTimeoutSeconds', 'streamIdleTimeoutSeconds']) {
      const input = element.elements.namedItem(name) as HTMLInputElement;
      expect(input.value).toBe('0');
      expect(input.checkValidity()).toBe(true);
    }
    expect(endpointValues(element)).toMatchObject({ requestTimeoutSeconds: 0, streamIdleTimeoutSeconds: 0 });
  }
});

test('saved limits can be retained or cleared', () => {
  const element = form({ id: 'test', name: 'Test', url: 'http://localhost:1234/v1', model: 'test', cloud: false, contextTokens: 32768, compact: true, requestTimeoutSeconds: 3600, streamIdleTimeoutSeconds: 900 });
  expect(endpointValues(element)).toMatchObject({ requestTimeoutSeconds: 3600, streamIdleTimeoutSeconds: 900 });
  (element.elements.namedItem('requestTimeoutSeconds') as HTMLInputElement).value = '';
  (element.elements.namedItem('streamIdleTimeoutSeconds') as HTMLInputElement).value = '0';
  const values = endpointValues(element);
  expect(values.requestTimeoutSeconds).toBeUndefined();
  expect(values.streamIdleTimeoutSeconds).toBe(0);
});
