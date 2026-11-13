const assert = require('node:assert/strict');

function resetToken(text, frontendUrl) {
  const links = text.match(/https?:\/\/[^\s<>"']+/g) || [];
  const link = links.find(value => value.includes('reset-password'));
  assert.ok(link, 'Recovery email must contain an absolute reset URL');
  const url = new URL(link);
  assert.equal(url.origin, new URL(frontendUrl).origin, 'Recovery URL must use the configured frontend');
  assert.equal(url.pathname, '/auth/reset-password', 'Recovery URL must target the frontend auth route');
  assert.equal(url.hash, '');
  const token = url.searchParams.get('token');
  assert.match(token || '', /^[a-f0-9]{64}$/, 'Recovery URL must contain a complete token');
  return token;
}

async function emailText(axios, mailpitUrl, email, matches, excludeText = '') {
  const deadline = Date.now() + 10000;
  while (Date.now() < deadline) {
    const response = await axios.get(`${mailpitUrl}/api/v1/search?query=to:${encodeURIComponent(email)}`);
    for (const message of response.data.messages || []) {
      const recipients = message.To || [];
      if (!recipients.some(recipient => String(recipient.Address || recipient).toLowerCase() === email.toLowerCase())) continue;
      const detail = await axios.get(`${mailpitUrl}/api/v1/message/${message.ID}`);
      if (detail.data.Text !== excludeText && matches.test(detail.data.Text || '')) return detail.data.Text;
    }
    await new Promise(resolve => setTimeout(resolve, 200));
  }
  throw new Error('Timed out waiting for the expected test email');
}

module.exports = {resetToken, emailText};
