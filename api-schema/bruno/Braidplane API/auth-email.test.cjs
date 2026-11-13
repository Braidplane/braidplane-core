const {test} = require('node:test');
const assert = require('node:assert/strict');
const {resetToken, emailText} = require('./auth-email.cjs');
const frontend = 'http://localhost:3000';
const token = 'a'.repeat(64);
test('reset URL requires frontend origin, auth route and complete token', () => {
  assert.equal(resetToken(`Reset: ${frontend}/auth/reset-password?token=${token}`, frontend), token);
  for (const link of [
    `${frontend}/reset-password?token=${token}`,
    `https://wrong.test/auth/reset-password?token=${token}`,
    `${frontend}/auth/reset-password?token=short`,
    `/auth/reset-password?token=${token}`,
  ]) assert.throws(() => resetToken(link, frontend));
});
test('email polling selects expected message instead of unrelated latest mail', async () => {
  const axios = {get: async url => ({data: url.includes('/message/')
    ? {Text: url.endsWith('/reset') ? 'reset-password' : 'verification'}
    : {messages: [{ID: 'unrelated', To: [{Address: 'other@example.test'}]}, {ID: 'verify', To: [{Address: 'test@example.test'}]}, {ID: 'reset', To: [{Address: 'test@example.test'}]}]}})};
  assert.equal(await emailText(axios, 'http://mailpit', 'test@example.test', /reset-password/), 'reset-password');
});

test('verification resend waits for a different email', async () => {
  let calls = 0;
  const axios = {get: async url => ({data: url.includes('/message/')
    ? {Text: ++calls === 1 ? 'old token=abc' : 'new token=def'}
    : {messages: [{ID: 'verification', To: [{Address: 'test@example.test'}]}]}})};
  assert.equal(await emailText(axios, 'http://mailpit', 'test@example.test', /token=/, 'old token=abc'), 'new token=def');
});
