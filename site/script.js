for (const button of document.querySelectorAll('[data-copy]')) {
  button.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(button.dataset.copy);
      button.textContent = 'Copied!';
      document.querySelector('#copy-status').textContent = 'Command copied to clipboard.';
      setTimeout(() => { button.textContent = 'Copy'; }, 2000);
    } catch {
      document.querySelector('#copy-status').textContent = 'Copy unavailable. Select and copy the command manually.';
      button.textContent = 'Select text';
    }
  });
}
