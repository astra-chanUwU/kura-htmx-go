(() => {
  const csrf = () => document.querySelector('meta[name="kura-csrf"]')?.content || '';
  const errorBox = () => document.querySelector('[data-passkey-error]');
  const decode = (value) => {
    const base64 = value.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(value.length / 4) * 4, '=');
    return Uint8Array.from(atob(base64), (char) => char.charCodeAt(0));
  };
  const creationOptions = (value) => {
    if (PublicKeyCredential.parseCreationOptionsFromJSON) return PublicKeyCredential.parseCreationOptionsFromJSON(value);
    value.challenge = decode(value.challenge);
    value.user.id = decode(value.user.id);
    value.excludeCredentials = (value.excludeCredentials || []).map((item) => ({ ...item, id: decode(item.id) }));
    return value;
  };
  const requestOptions = (value) => {
    if (PublicKeyCredential.parseRequestOptionsFromJSON) return PublicKeyCredential.parseRequestOptionsFromJSON(value);
    value.challenge = decode(value.challenge);
    value.allowCredentials = (value.allowCredentials || []).map((item) => ({ ...item, id: decode(item.id) }));
    return value;
  };
  const encode = (buffer) => btoa(String.fromCharCode(...new Uint8Array(buffer))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  const credentialJSON = (credential) => credential.toJSON ? credential.toJSON() : {
    id: credential.id,
    rawId: encode(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: credential.response.attestationObject ? {
      clientDataJSON: encode(credential.response.clientDataJSON),
      attestationObject: encode(credential.response.attestationObject),
      transports: credential.response.getTransports ? credential.response.getTransports() : [],
    } : {
      clientDataJSON: encode(credential.response.clientDataJSON),
      authenticatorData: encode(credential.response.authenticatorData),
      signature: encode(credential.response.signature),
      userHandle: credential.response.userHandle ? encode(credential.response.userHandle) : null,
    },
  };
  const passkeyDefaultName = (username) => username.trim() ? `Kura passkey for ${username.trim()}` : 'Kura passkey';
  const recoveryDetails = (username, code) => [
    'Kura account recovery',
    `Username: ${username.trim()}`,
    'Purpose: Recover this account or replace its sign-in methods.',
    `Recovery code: ${code}`,
    'Replacing or recovering this account rotates the recovery code; the previous code is no longer valid.',
  ].join('\n');
  const recoveryDownloadFilename = (username) => {
    const safe = username.trim().replace(/[^A-Za-z0-9_-]+/g, '_');
    return `kura-recovery-${safe || 'account'}.txt`;
  };
  const usernameFor = (button) => button?.dataset.username ? document.querySelector(button.dataset.username)?.value || '' : '';
  const passkeyNameSyncs = new WeakMap();
  function bindPasskeyDefault(button) {
    const name = document.querySelector(button.dataset.name);
    const username = document.querySelector(button.dataset.username);
    if (!name || !username) return;
    let lastGenerated = name.value;
    let generated = name.value === passkeyDefaultName('') || name.value === passkeyDefaultName(username.value);
    const update = () => {
      if (!generated && name.value !== lastGenerated) return;
      name.value = passkeyDefaultName(username.value);
      lastGenerated = name.value;
      generated = true;
    };
    username.addEventListener('input', update);
    name.addEventListener('input', () => {
      if (name.value !== lastGenerated) generated = false;
    });
    update();
    passkeyNameSyncs.set(button, update);
  }
  async function post(url, body, challenge, extraHeaders) {
    const response = await fetch(url, {
      method: 'POST', credentials: 'same-origin', body: JSON.stringify(body || {}),
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf(), ...(challenge ? { 'X-Kura-Challenge': challenge } : {}), ...(extraHeaders || {}) },
    });
    if (!response.ok) {
      let message = 'Passkey request failed.';
      try {
        const payload = await response.json();
        if (typeof payload.error === 'string' && payload.error) message = payload.error;
      } catch (_) {
        // Keep network or legacy server failures generic.
      }
      throw new Error(message);
    }
    return response.json();
  }
  function showRecovery(code, username) {
    const panel = document.querySelector('[data-recovery-result]');
    if (!panel || !code) return false;
    const account = (username || panel.dataset.recoveryUsername || '').trim();
    panel.hidden = false;
    panel.dataset.recoveryUsername = account;
    panel.dataset.recoveryFilename = recoveryDownloadFilename(account);
    panel.querySelector('[data-recovery-code]').textContent = code;
    panel.querySelector('[data-recovery-details]').textContent = recoveryDetails(account, code);
    panel.scrollIntoView({ block: 'center' });
    return true;
  }
  async function copyRecovery(button, codeOnly) {
    const panel = button.closest('.recovery-code');
    const value = panel?.querySelector(codeOnly ? '[data-recovery-code]' : '[data-recovery-details]')?.textContent || '';
    if (!value) return;
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(value);
    } else {
      const field = document.createElement('textarea');
      field.value = value;
      document.body.append(field);
      field.select();
      document.execCommand('copy');
      field.remove();
    }
    const label = button.textContent;
    button.textContent = 'Copied';
    window.setTimeout(() => { button.textContent = label; }, 1500);
  }
  async function run(button) {
    const action = button.dataset.passkeyAction;
    const isCreate = action === 'register' || action === 'add' || action === 'bootstrap' || action === 'recover';
    const endpoints = {
      register: ['/auth/passkeys/register/begin', '/auth/passkeys/register/finish'],
      add: ['/account/passkeys/add/begin', '/account/passkeys/add/finish'],
      login: ['/auth/passkeys/login/begin', '/auth/passkeys/login/finish' + location.search],
      fresh: ['/auth/passkeys/fresh/begin', '/auth/passkeys/fresh/finish'],
      bootstrap: ['/setup/passkey/begin', '/setup/passkey/finish'],
      recover: ['/recover/passkey/begin', '/recover/passkey/finish'],
    }[action];
    passkeyNameSyncs.get(button)?.();
    const input = {};
    if (button.dataset.username) input.username = document.querySelector(button.dataset.username)?.value || '';
    if (button.dataset.name) input.name = document.querySelector(button.dataset.name)?.value || '';
    if (button.dataset.password) input.password = document.querySelector(button.dataset.password)?.value || '';
    if (button.dataset.recovery) input.recoveryCode = document.querySelector(button.dataset.recovery)?.value || '';
    if (button.dataset.invite) input.invite = button.dataset.invite;
    const extraHeaders = button.dataset.token ? { 'X-Kura-Bootstrap': button.dataset.token } : {};
    button.disabled = true;
    const error = errorBox();
    if (error) error.hidden = true;
    try {
      const begun = await post(endpoints[0], input, '', extraHeaders);
      const publicKey = isCreate ? creationOptions(begun.options.publicKey) : requestOptions(begun.options.publicKey);
      const credential = isCreate ? await navigator.credentials.create({ publicKey }) : await navigator.credentials.get({ publicKey });
      const result = await post(endpoints[1], credentialJSON(credential), begun.challengeToken, extraHeaders);
      if (!showRecovery(result.recoveryCode, usernameFor(button))) {
        if (result.redirect) location.assign(result.redirect);
        else location.reload();
      }
    } catch (reason) {
      if (error) {
        error.textContent = reason.name === 'NotAllowedError' ? 'Passkey request cancelled.' : reason.message;
        error.hidden = false;
      }
    } finally {
      button.disabled = false;
    }
  }
  document.querySelectorAll('[data-passkey-action][data-name][data-username]').forEach(bindPasskeyDefault);
  document.addEventListener('click', async (event) => {
    const button = event.target.closest('[data-passkey-action]');
    if (button) run(button);
    const copy = event.target.closest('[data-copy-recovery]');
    if (copy) {
      try { await copyRecovery(copy); } catch { copy.textContent = 'Copy failed'; }
    }
    const copyCode = event.target.closest('[data-copy-recovery-code]');
    if (copyCode) {
      try { await copyRecovery(copyCode, true); } catch { copyCode.textContent = 'Copy failed'; }
    }
    const download = event.target.closest('[data-download-recovery]');
    if (download) {
      const panel = download.closest('.recovery-code');
      const details = panel?.querySelector('[data-recovery-details]')?.textContent || '';
      const username = panel?.dataset.recoveryUsername || '';
      const link = document.createElement('a');
      link.href = URL.createObjectURL(new Blob([details + '\n'], { type: 'text/plain' }));
      link.download = panel?.dataset.recoveryFilename || recoveryDownloadFilename(username);
      link.click();
      URL.revokeObjectURL(link.href);
    }
    const copyInvite = event.target.closest('[data-copy-invite], [data-copy-invite-code]');
    if (copyInvite) {
      const value = copyInvite.matches('[data-copy-invite-code]')
        ? document.querySelector('[data-invite-code]')?.textContent || ''
        : document.querySelector('[data-invite-url]')?.href || '';
      if (value && navigator.clipboard?.writeText) {
        try { await navigator.clipboard.writeText(value); copyInvite.textContent = 'Copied'; } catch (_) { copyInvite.textContent = 'Copy failed'; }
      }
    }
  });
})();
