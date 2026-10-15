import { describe, expect, test } from 'vitest';
import { describeDevice } from '../src/devices.js';

describe('describeDevice', () => {
  test('our app, with and without its OS; browsers; nothing', () => {
    expect(describeDevice('runer-app/0.1.0 (macos)')).toEqual({ name: 'Mac · runer app', kind: 'laptop' });
    expect(describeDevice('runer-app/0.1.0 (ios)')).toEqual({ name: 'iPhone · runer app', kind: 'phone' });
    expect(describeDevice('runer-app/0.1.0')).toEqual({ name: 'runer app', kind: 'laptop' });
    expect(describeDevice('Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1')).toEqual({ name: 'iPhone · Safari', kind: 'phone' });
    expect(describeDevice('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/120.0 Safari/537.36')).toEqual({ name: 'Mac · Chrome', kind: 'browser' });
    expect(describeDevice(null)).toBeNull();
  });
});
