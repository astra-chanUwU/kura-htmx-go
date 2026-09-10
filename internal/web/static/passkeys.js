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
  async function post(url, body, challenge, extraHeaders) {
    const response = await fetch(url, {
      method: 'POST', credentials: 'same-origin', body: JSON.stringify(body || {}),
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf(), ...(challenge ? { 'X-Kura-Challenge': challenge } : {}), ...(extraHeaders || {}) },
    });
    if (!response.ok) throw new Error((await response.text()).trim() || 'Passkey request failed');
    return response.json();
  }
  function showRecovery(code) {
    const panel = document.querySelector('[data-recovery-result]');
    if (!panel || !code) return false;
    panel.hidden = false;
    panel.querySelector('[data-recovery-code]').textContent = code;
    panel.scrollIntoView({ block: 'center' });
    return true;
  }
  async function copyRecovery(button) {
    const code = button.closest('.recovery-code')?.querySelector('[data-recovery-code]')?.textContent || '';
    if (!code) return;
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(code);
    } else {
      const field = document.createElement('textarea');
      field.value = code;
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
    const input = {};
    if (button.dataset.username) input.username = document.querySelector(button.dataset.username)?.value || '';
    if (button.dataset.name) input.name = document.querySelector(button.dataset.name)?.value || '';
    if (button.dataset.password) input.password = document.querySelector(button.dataset.password)?.value || '';
    if (button.dataset.recovery) input.recoveryCode = document.querySelector(button.dataset.recovery)?.value || '';
    const extraHeaders = button.dataset.token ? { 'X-Kura-Bootstrap': button.dataset.token } : {};
    button.disabled = true;
    const error = errorBox();
    if (error) error.hidden = true;
    try {
      const begun = await post(endpoints[0], input, '', extraHeaders);
      const publicKey = isCreate ? creationOptions(begun.options.publicKey) : requestOptions(begun.options.publicKey);
      const credential = isCreate ? await navigator.credentials.create({ publicKey }) : await navigator.credentials.get({ publicKey });
      const result = await post(endpoints[1], credentialJSON(credential), begun.challengeToken, extraHeaders);
      if (!showRecovery(result.recoveryCode)) {
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
  document.querySelectorAll('[data-download-recovery]').forEach((download) => {
    const copy = document.createElement('button');
    copy.type = 'button';
    copy.dataset.copyRecovery = '';
    copy.textContent = 'Copy code';
    download.before(copy);
  });
  document.addEventListener('click', async (event) => {
    const button = event.target.closest('[data-passkey-action]');
    if (button) run(button);
    const copy = event.target.closest('[data-copy-recovery]');
    if (copy) {
      try { await copyRecovery(copy); } catch { copy.textContent = 'Copy failed'; }
    }
    const download = event.target.closest('[data-download-recovery]');
    if (download) {
      const code = download.closest('.recovery-code')?.querySelector('[data-recovery-code]')?.textContent || '';
      const link = document.createElement('a');
      link.href = URL.createObjectURL(new Blob([code + '\n'], { type: 'text/plain' }));
      link.download = 'kura-recovery-code.txt';
      link.click();
      URL.revokeObjectURL(link.href);
    }
  });
})();
