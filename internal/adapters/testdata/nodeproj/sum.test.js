const { add } = require('./sum');

test('adds numbers', () => {
  expect(add(1, 2)).toBe(3);
});

test.skip('is pending', () => {
  // Skipped on purpose: a test that does not run proves nothing, so the
  // adapter has to report it as skipped rather than as a pass.
  expect(add(1, 2)).toBe(4);
});

test('fails', () => {
  expect(add(1, 2)).toBe(4);
});
