"use strict";

// Keep setup presentation and copying identical in both Buddy apps.
(() => {
  const setup = document.querySelector('main[data-view="setup"]');
  if (!setup) return;
  const panels = Array.from(setup.querySelectorAll('.setup-printer'));
  const choice = setup.querySelector('#setup-printer');
  const status = setup.querySelector('#setup-copy-status');
  function showPrinter() {
    let id;
    try { id = decodeURIComponent(window.location.hash.slice(1)); } catch { id = ''; }
    const selected = panels.find(panel => panel.id === id) || panels[0];
    if (!selected) return;
    choice.value = selected.id;
    for (const panel of panels) panel.hidden = panel !== selected;
    status.textContent = '';
  }
  if (panels.length) {
    choice.addEventListener('change', () => {
      const url = new URL(window.location.href);
      url.hash = choice.value;
      window.history.pushState(null, '', url);
      showPrinter();
    });
    window.addEventListener('hashchange', showPrinter);
    window.addEventListener('popstate', showPrinter);
    setup.classList.add('setup-enhanced');
    setup.querySelector('.printer-choice').hidden = false;
    showPrinter();
  }
  for (const code of setup.querySelectorAll('.code-panel code')) {
    // Add color using text nodes only; preserve every character and newline.
    const lines = code.textContent.split('\n');
    code.replaceChildren();
    lines.forEach((line, i) => {
      if (i) code.append(document.createTextNode('\n'));
      const command = line.match(/^([MG]\d+)(.*)$/);
      if (line.startsWith(';') || command) {
        const span = document.createElement('span');
        span.className = command ? 'command' : 'comment';
        span.textContent = command ? command[1] : line;
        code.append(span);
        if (command) code.append(document.createTextNode(command[2]));
      } else code.append(document.createTextNode(line));
    });
  }
  for (const button of setup.querySelectorAll('[data-copy]')) {
    const label = button.textContent;
    let timer;
    button.hidden = false;
    button.addEventListener('click', async () => {
      const code = document.getElementById(button.dataset.copy);
      const selectCode = () => {
        const range = document.createRange(); range.selectNodeContents(code);
        const selection = window.getSelection(); selection.removeAllRanges(); selection.addRange(range);
      };
      clearTimeout(timer);
      try {
        if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(code.textContent);
        else {
          selectCode();
          if (!document.execCommand('copy')) throw new Error('Copy unavailable');
          window.getSelection().removeAllRanges();
        }
        button.textContent = 'Copied';
        status.textContent = `${label.replace('Copy', 'Copied')} to clipboard.`;
        timer = setTimeout(() => { button.textContent = label; }, 2500);
      } catch {
        selectCode();
        button.textContent = 'Copy manually';
        status.textContent = 'G-code selected. Copy it with your browser.';
        timer = setTimeout(() => { button.textContent = label; }, 5000);
      }
    });
  }
})();
