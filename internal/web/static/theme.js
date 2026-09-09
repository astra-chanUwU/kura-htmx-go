// Apply the saved palette before the stylesheet paints. Dark is always the default.
(() => {
  const root = document.documentElement;
  const key = 'kura-theme';
  let theme = 'dark';
  try {
    if (localStorage.getItem(key) === 'light') theme = 'light';
  } catch (_) { /* Storage can be unavailable in private browsing. */ }
  root.dataset.theme = theme;

  function updateButton() {
    const button = document.querySelector('[data-theme-toggle]');
    if (!button) return;
    const dark = root.dataset.theme === 'dark';
    button.setAttribute('aria-pressed', String(!dark));
    button.setAttribute('aria-label', 'Light theme');
    button.textContent = dark ? 'Light theme' : 'Dark theme';
    button.title = `Switch to ${dark ? 'Magic Girl' : 'Dark Magic Girl'}`;
  }

  document.addEventListener('DOMContentLoaded', () => {
    const button = document.querySelector('[data-theme-toggle]');
    if (!button) return;
    button.hidden = false;
    updateButton();
    button.addEventListener('click', () => {
      root.dataset.theme = root.dataset.theme === 'dark' ? 'light' : 'dark';
      try { localStorage.setItem(key, root.dataset.theme); } catch (_) {}
      updateButton();
    });
  });
  window.addEventListener('storage', event => {
    if (event.key !== key && event.key !== null) return;
    root.dataset.theme = event.newValue === 'light' ? 'light' : 'dark';
    updateButton();
  });
})();
