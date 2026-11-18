const { test } = require('node:test');
const assert = require('node:assert/strict');
const { resetToken } = require('./auth-email.cjs');

test('a clean recovery URL and separate code are both required', () => {
  const frontend = 'https://frontend.test';
  const code = 'a'.repeat(64);
  assert.equal(resetToken(`${frontend}/auth/reset-password\n\nCode: ${code}`, frontend), code);

  for (const text of [
    `${frontend}/auth/reset-password?token=${code}\nCode: ${code}`,
    `${frontend}/reset-password\nCode: ${code}`,
    `https://wrong.test/auth/reset-password\nCode: ${code}`,
    `${frontend}/auth/reset-password\nCode: short`,
    `/auth/reset-password\nCode: ${code}`,
  ]) assert.throws(() => resetToken(text, frontend));
});
