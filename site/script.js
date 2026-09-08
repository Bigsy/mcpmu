const status = document.querySelector('#copy-status');

function selectCommand(button) {
  const code = button.parentElement.querySelector('code');
  if (!code) return;
  const range = document.createRange();
  range.selectNodeContents(code);
  const selection = window.getSelection();
  selection.removeAllRanges();
  selection.addRange(range);
}

for (const button of document.querySelectorAll('[data-copy]')) {
  const label = button.textContent;
  button.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(button.dataset.copy);
      button.textContent = 'Copied!';
      button.classList.add('copied');
      status.textContent = 'Command copied to clipboard.';
    } catch {
      selectCommand(button);
      button.textContent = 'Selected';
      status.textContent = 'Copy unavailable. The command is selected; press Ctrl+C or Cmd+C to copy.';
    }
    setTimeout(() => { button.textContent = label; button.classList.remove('copied'); }, 2000);
  });
}
