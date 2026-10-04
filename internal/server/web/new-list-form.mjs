// Use page controls: embedded web views may not implement window.prompt().
export function bindNewListForm({form, input, error, submit, cancel, toggle, create, created}) {
  let pending = false;
  function close() {
    if (pending) return;
    form.hidden = true;
    toggle.setAttribute('aria-expanded', 'false');
    toggle.focus();
  }
  toggle.onclick = () => {
    if (pending) return;
    if (!form.hidden) { close(); return; }
    form.hidden = false;
    input.value = '';
    error.textContent = '';
    toggle.setAttribute('aria-expanded', 'true');
    input.focus();
  };
  cancel.onclick = close;
  form.onkeydown = event => {
    if (event.key === 'Escape') { event.preventDefault(); close(); }
  };
  form.onsubmit = async event => {
    event.preventDefault();
    if (pending) return;
    const name = input.value.trim().toUpperCase();
    if (!name) {
      error.textContent = 'Enter a list name.';
      input.focus();
      return;
    }
    pending = true;
    submit.disabled = cancel.disabled = input.disabled = toggle.disabled = true;
    submit.textContent = 'SAVING…';
    error.textContent = '';
    let saved = false;
    try {
      await create(name);
      saved = true;
    } catch (failure) {
      error.textContent = failure.message || 'Could not save the list. Try again.';
    } finally {
      pending = false;
      submit.disabled = cancel.disabled = input.disabled = toggle.disabled = false;
      submit.textContent = 'CREATE LIST';
    }
    if (saved) { close(); created(); }
    else input.focus();
  };
}
