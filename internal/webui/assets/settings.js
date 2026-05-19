  // Caret toggles open/close on integration cards.
  document.querySelectorAll('.integration .caret').forEach(btn => {
    btn.addEventListener('click', () => {
      btn.closest('.integration').toggleAttribute('open');
    });
  });

  // Dev-mode "Enable" buttons that don't kick off OAuth: just open
  // the card and focus the first relevant input so the user can paste.
  document.querySelectorAll('[data-toggle-card]').forEach(btn => {
    btn.addEventListener('click', () => {
      const card = btn.closest('.integration');
      card.setAttribute('open', '');
      const field = btn.dataset.focusField;
      if (field) {
        const input = document.getElementById(field);
        if (input) setTimeout(() => input.focus(), 0);
      }
    });
  });

  // API-key integration: Enable button opens its modal.
  document.querySelectorAll('[data-open-modal]').forEach(btn => {
    btn.addEventListener('click', () => {
      const dlg = document.getElementById(btn.dataset.openModal);
      if (dlg && typeof dlg.showModal === 'function') {
        dlg.showModal();
        const input = dlg.querySelector('input[type="password"]');
        if (input) setTimeout(() => input.focus(), 0);
      }
    });
  });
  document.querySelectorAll('[data-close-modal]').forEach(btn => {
    btn.addEventListener('click', () => btn.closest('dialog').close());
  });

  // Integration disconnect confirms use the shared app-dialog styling
  // instead of native browser confirm() prompts.
  (function () {
    const dlg = document.getElementById('integration-disconnect-dialog');
    if (!dlg) return;
    const title = document.getElementById('integration-disconnect-title');
    const message = document.getElementById('integration-disconnect-message');
    const cancel = document.getElementById('integration-disconnect-cancel');
    const confirm = document.getElementById('integration-disconnect-confirm');
    let pendingForm = null;

    function resetDialog() {
      pendingForm = null;
      if (confirm) {
        confirm.disabled = false;
        confirm.textContent = 'Disconnect';
      }
    }

    function submitPending() {
      if (!pendingForm) return;
      const form = pendingForm;
      pendingForm = null;
      form.dataset.confirmed = '1';
      if (confirm) {
        confirm.disabled = true;
        confirm.textContent = 'Disconnecting…';
      }
      if (typeof form.requestSubmit === 'function') {
        form.requestSubmit();
      } else {
        form.submit();
      }
    }

    document.querySelectorAll('form[data-confirm-title][data-confirm-message]').forEach(form => {
      form.addEventListener('submit', e => {
        if (form.dataset.confirmed === '1') return;
        e.preventDefault();
        pendingForm = form;
        if (title) title.textContent = form.dataset.confirmTitle || 'Disconnect integration?';
        if (message) message.textContent = form.dataset.confirmMessage || 'This integration will be disconnected.';
        if (confirm) {
          confirm.disabled = false;
          confirm.textContent = 'Disconnect';
        }
        if (typeof dlg.showModal === 'function') {
          dlg.showModal();
        } else {
          submitPending();
        }
      });
    });

    if (cancel) cancel.addEventListener('click', () => dlg.close());
    if (confirm) confirm.addEventListener('click', submitPending);
    dlg.addEventListener('close', resetDialog);
  })();

  // Org delete dialog (General tab → Danger zone). Same app-dialog
  // pattern as the integration-disconnect flow: intercept the submit,
  // open the dialog, only submit when the user confirms.
  (function () {
    const form = document.getElementById('org-delete-form');
    const dlg = document.getElementById('org-delete-dialog');
    if (!form || !dlg) return;
    const cancel = document.getElementById('org-delete-cancel');
    const confirm = document.getElementById('org-delete-confirm');
    let confirmed = false;
    form.addEventListener('submit', e => {
      if (confirmed) return;
      e.preventDefault();
      if (confirm) {
        confirm.disabled = false;
        confirm.textContent = 'Delete organization';
      }
      if (typeof dlg.showModal === 'function') {
        dlg.showModal();
      } else {
        confirmed = true;
        form.submit();
      }
    });
    if (cancel) cancel.addEventListener('click', () => dlg.close());
    if (confirm) confirm.addEventListener('click', () => {
      confirmed = true;
      confirm.disabled = true;
      confirm.textContent = 'Deleting…';
      if (typeof form.requestSubmit === 'function') {
        form.requestSubmit();
      } else {
        form.submit();
      }
    });
    // Reset the button and confirmation flag on close so Cancel/Esc and
    // network-level submit failures always require a fresh confirmation.
    dlg.addEventListener('close', () => {
      confirmed = false;
      if (confirm) {
        confirm.disabled = false;
        confirm.textContent = 'Delete organization';
      }
    });
  })();

  // Existing rotate/remove logic for the "secret" sub-template.
  document.querySelectorAll('[data-rotate]').forEach(btn => {
    btn.addEventListener('click', () => {
      const field = btn.dataset.rotate;
      document.getElementById(field + '-saved').style.display = 'none';
      document.getElementById(field + '-rotate').style.display = '';
      document.getElementById(field).focus();
    });
  });
  document.querySelectorAll('[data-cancel]').forEach(btn => {
    btn.addEventListener('click', () => {
      const field = btn.dataset.cancel;
      document.getElementById(field + '-rotate').style.display = 'none';
      document.getElementById(field + '-saved').style.display = '';
      document.getElementById(field).value = '';
    });
  });
  document.querySelectorAll('[data-remove]').forEach(btn => {
    btn.addEventListener('click', () => {
      const field = btn.dataset.remove;
      if (!confirm('Remove the saved ' + field + '? Requests that need it will fail until a new value is saved.')) return;
      document.getElementById(field + '-action').value = 'remove';
      document.getElementById(field + '-preview').classList.add('removing');
      btn.textContent = 'Will be removed on Save';
      btn.disabled = true;
    });
  });

  // Credential tabs (Anthropic integration).
  // Mutually exclusive tabs: only the active panel's inputs are
  // submittable. Hidden panels get `disabled` set on every input and
  // hidden field — without this, both panels' values post on every
  // save (display:none doesn't suppress submission), so a stale
  // `_action=remove` from the inactive tab could nuke a saved value
  // the user can't even see, and a user submitting via Enter from a
  // programmatically-focused hidden field could hit either tab's
  // handler. Server-side already clears "the other" credential when
  // a new one is pasted, but disabling here keeps the wire form
  // honest.
  // Tab buttons live outside panels; tab-internal buttons (rotate
  // /remove/cancel) and inputs all need to follow the panel's
  // active/inactive state.
  function setCredPanelDisabled(panel, disabled) {
    panel.querySelectorAll('input, button[data-rotate], button[data-remove], button[data-cancel]').forEach(el => {
      el.disabled = disabled;
    });
  }
  document.querySelectorAll('[data-cred-panel]').forEach(p => {
    setCredPanelDisabled(p, !p.classList.contains('active'));
  });
  document.querySelectorAll('[data-cred-tab]').forEach(btn => {
    btn.addEventListener('click', () => {
      const tab = btn.dataset.credTab;
      const form = btn.closest('form');
      if (!form) return;
      form.querySelectorAll('[data-cred-tab]').forEach(t => {
        const active = t.dataset.credTab === tab;
        t.classList.toggle('active', active);
        t.setAttribute('aria-selected', active ? 'true' : 'false');
      });
      form.querySelectorAll('[data-cred-panel]').forEach(p => {
        const active = p.dataset.credPanel === tab;
        p.classList.toggle('active', active);
        setCredPanelDisabled(p, !active);
      });
    });
  });

  // Members-tab role select: only reveal Save when value changes.
  document.querySelectorAll('.role-form select').forEach(sel => {
    const save = sel.parentElement.querySelector('.role-save');
    sel.addEventListener('change', () => {
      save.hidden = (sel.value === sel.dataset.original);
    });
  });

  // Confirm-on-submit for member remove + invitation revoke.
  document.querySelectorAll('.remove-form').forEach(f => {
    f.addEventListener('submit', e => {
      const email = f.dataset.email || 'this member';
      if (!confirm('Remove ' + email + ' from this organization?')) e.preventDefault();
    });
  });
  document.querySelectorAll('.revoke-form').forEach(f => {
    f.addEventListener('submit', e => {
      const email = f.dataset.email || 'this invitation';
      if (!confirm('Revoke invitation to ' + email + '?')) e.preventDefault();
    });
  });
  document.querySelectorAll('.api-key-revoke-form').forEach(f => {
    f.addEventListener('submit', e => {
      const name = f.dataset.name || 'this API key';
      if (!confirm('Revoke ' + name + '?')) e.preventDefault();
    });
  });
