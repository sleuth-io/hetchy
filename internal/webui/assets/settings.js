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
        const input = dlg.querySelector('[autofocus], input[type="password"], input:not([type="hidden"]), select, textarea');
        if (input) setTimeout(() => input.focus(), 0);
      }
    });
  });
  document.querySelectorAll('[data-close-modal]').forEach(btn => {
    btn.addEventListener('click', () => btn.closest('dialog').close());
  });

  // Agent templates carry default prompts and skill names; mirror those
  // defaults into the create modal when a template is selected.
  (function () {
    const form = document.getElementById('agent-create-form');
    const template = document.getElementById('agent-template');
    const prompt = document.getElementById('agent-create-prompt');
    const picker = form ? form.querySelector('[data-agent-skill-picker]') : null;
    if (!form || !template || !picker) return;

    const search = picker.querySelector('[data-agent-skill-search]');
    const toggle = picker.querySelector('[data-agent-skill-toggle]');
    const menu = picker.querySelector('[data-agent-skill-menu]');
    const selectedEl = picker.querySelector('[data-agent-skill-selected]');
    const hiddenEl = picker.querySelector('[data-agent-skill-hidden]');
    const optionEls = Array.from(picker.querySelectorAll('[data-agent-skill-option]'));
    const skills = optionEls.map(el => ({
      name: el.dataset.skillName || '',
      displayName: el.dataset.skillDisplayName || el.dataset.skillName || '',
      normalizedName: normalizeSkillName(el.dataset.skillName || ''),
      description: el.dataset.skillDescription || '',
      source: el.dataset.skillSource || '',
      el
    })).filter(skill => skill.name);
    const byName = new Map(skills.map(skill => [skill.name, skill]));
    const byNormalizedName = new Map(skills.map(skill => [skill.normalizedName, skill]));
    const selectedSkills = new Set();
    let suppressSearchFocusOpen = false;

    function normalizeSkillName(value) {
      return (value || '').trim().toLowerCase();
    }

    function splitSkills(value) {
      return (value || '').split(',').map(s => s.trim()).filter(Boolean);
    }

    function resolveSkillName(name) {
      const trimmed = (name || '').trim();
      if (byName.has(trimmed)) return trimmed;

      const normalized = normalizeSkillName(trimmed);
      if (!normalized) return '';
      const exactNormalized = byNormalizedName.get(normalized);
      if (exactNormalized) return exactNormalized.name;

      // Skills imported from the public Git Vault can be prefixed by the
      // vault slug in Skills.new, while the agent templates keep the original
      // skill names. Match that stable suffix so templates still hydrate.
      const suffix = '-' + normalized;
      const matches = skills.filter(skill => skill.normalizedName.endsWith(suffix));
      if (matches.length === 0) return '';
      matches.sort((a, b) => a.name.length - b.name.length || a.name.localeCompare(b.name));
      return matches[0].name;
    }

    function setOpen(open) {
      if (!menu || !search || skills.length === 0) return;
      menu.hidden = !open;
      search.setAttribute('aria-expanded', open ? 'true' : 'false');
      if (toggle) toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
      if (open) {
        renderOptions();
        picker.scrollIntoView({ block: 'nearest' });
      }
    }

    function focusSearchWithoutOpening() {
      if (!search) return;
      suppressSearchFocusOpen = true;
      search.focus();
      window.setTimeout(() => {
        suppressSearchFocusOpen = false;
      }, 0);
    }

    function renderOptions() {
      const query = (search ? search.value : '').trim().toLowerCase();
      for (const skill of skills) {
        const haystack = (skill.name + ' ' + skill.displayName + ' ' + skill.source + ' ' + skill.description).toLowerCase();
        const hidden = selectedSkills.has(skill.name) || !!(query && !haystack.includes(query));
        skill.el.hidden = hidden;
        skill.el.style.display = hidden ? 'none' : '';
      }
    }

    function renderSelected() {
      if (!selectedEl || !hiddenEl) return;
      selectedEl.innerHTML = '';
      hiddenEl.innerHTML = '';
      for (const name of selectedSkills) {
        const input = document.createElement('input');
        input.type = 'hidden';
        input.name = 'skills';
        input.value = name;
        hiddenEl.appendChild(input);

        const skill = byName.get(name);
        const label = skill ? skill.displayName : name;
        const chip = document.createElement('span');
        chip.className = 'agent-skill-chip';
        chip.textContent = label;
        const remove = document.createElement('button');
        remove.type = 'button';
        remove.setAttribute('aria-label', 'Remove ' + label);
        remove.textContent = 'x';
        remove.addEventListener('click', () => {
          selectedSkills.delete(name);
          renderSelected();
          renderOptions();
          focusSearchWithoutOpening();
        });
        chip.appendChild(remove);
        selectedEl.appendChild(chip);
      }
    }

    function selectSkill(name) {
      if (!byName.has(name)) return;
      selectedSkills.add(name);
      if (search) search.value = '';
      renderSelected();
      renderOptions();
      setOpen(false);
      focusSearchWithoutOpening();
    }

    function applyTemplate() {
      const selected = template.selectedOptions && template.selectedOptions[0];
      if (prompt) prompt.value = selected ? selected.dataset.prompt || '' : '';

      selectedSkills.clear();
      splitSkills(selected ? selected.dataset.skills : '').forEach(name => {
        const resolved = resolveSkillName(name);
        if (resolved) selectedSkills.add(resolved);
      });
      renderSelected();
      renderOptions();
    }

    optionEls.forEach(el => {
      el.addEventListener('click', () => selectSkill(el.dataset.skillName || ''));
    });
    if (search) {
      search.addEventListener('focus', () => {
        if (suppressSearchFocusOpen) {
          suppressSearchFocusOpen = false;
          return;
        }
        setOpen(true);
      });
      search.addEventListener('input', () => setOpen(true));
      search.addEventListener('keydown', e => {
        if (e.key === 'Escape') {
          setOpen(false);
          return;
        }
        if (e.key !== 'Enter') return;
        const first = optionEls.find(el => !el.hidden);
        if (!first) return;
        e.preventDefault();
        selectSkill(first.dataset.skillName || '');
      });
    }
    if (toggle) {
      toggle.addEventListener('click', () => {
        const opening = menu ? menu.hidden : true;
        setOpen(opening);
        if (opening && search) search.focus();
      });
    }
    document.addEventListener('click', e => {
      if (!picker.contains(e.target)) setOpen(false);
    });
    template.addEventListener('change', applyTemplate);
    renderSelected();
	  })();

	  // SX-backed agent actions can take several seconds. Keep the posted
	  // controls enabled, but lock the buttons and replace the submit label
	  // with a spinner so the modal shows progress before navigation.
	  (function () {
	    const forms = [
	      '#agent-create-form',
	      '.agent-command-form',
	      '.agent-upload-form',
	      'dialog[id^="modal-agent-team-"] form'
	    ].join(', ');

	    function setSubmitting(form, submitter) {
	      if (form.dataset.submitting === '1') return;
	      form.dataset.submitting = '1';
	      form.setAttribute('aria-busy', 'true');

	      const button = submitter && submitter.matches('button[type="submit"], button:not([type])')
	        ? submitter
	        : form.querySelector('button[type="submit"], button:not([type])');
	      form.querySelectorAll('button').forEach(btn => {
	        btn.disabled = true;
	      });
	      if (!button) return;
	      button.dataset.originalLabel = button.textContent;
	      button.classList.add('is-submitting');
	      button.setAttribute('aria-label', button.dataset.submittingLabel || 'Working');
	      button.innerHTML = '<span class="button-spinner" aria-hidden="true"></span>';
	    }

	    document.querySelectorAll(forms).forEach(form => {
	      form.addEventListener('submit', e => {
	        setSubmitting(form, e.submitter);
	      });
	    });
	  })();

	  // Destructive settings actions use the shared app-dialog styling
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
        confirm.textContent = 'Confirm';
      }
    }

    function submitPending() {
      if (!pendingForm) return;
      const form = pendingForm;
      pendingForm = null;
      form.dataset.confirmed = '1';
      if (confirm) {
        confirm.disabled = true;
        confirm.textContent = form.dataset.confirmProgress || 'Working...';
      }
      if (typeof form.requestSubmit === 'function') {
        form.requestSubmit();
      } else {
        form.submit();
      }
    }

    document.querySelectorAll('form[data-confirm-title][data-confirm-message]:not([data-billing-plan-confirm])').forEach(form => {
      form.addEventListener('submit', e => {
        if (form.dataset.confirmed === '1') return;
        e.preventDefault();
        pendingForm = form;
        if (title) title.textContent = form.dataset.confirmTitle || 'Disconnect integration?';
        if (message) message.textContent = form.dataset.confirmMessage || 'This integration will be disconnected.';
        if (confirm) {
          confirm.disabled = false;
          confirm.textContent = form.dataset.confirmAction || 'Disconnect';
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

  // Billing plan switches need explicit confirmation because upgrades
  // bill immediately and downgrades schedule a future Stripe change.
  (function () {
    const dlg = document.getElementById('billing-plan-switch-dialog');
    if (!dlg) return;
    const title = document.getElementById('billing-plan-switch-title');
    const message = document.getElementById('billing-plan-switch-message');
    const cancel = document.getElementById('billing-plan-switch-cancel');
    const confirm = document.getElementById('billing-plan-switch-confirm');
    let pendingForm = null;

    function resetDialog() {
      pendingForm = null;
      if (confirm) {
        confirm.disabled = false;
        confirm.textContent = 'Switch';
      }
    }

    function submitPending() {
      if (!pendingForm) return;
      const form = pendingForm;
      pendingForm = null;
      form.dataset.confirmed = '1';
      if (confirm) {
        confirm.disabled = true;
        confirm.textContent = 'Switching...';
      }
      if (typeof form.requestSubmit === 'function') {
        form.requestSubmit();
      } else {
        form.submit();
      }
    }

    document.querySelectorAll('form[data-billing-plan-confirm="1"]').forEach(form => {
      form.addEventListener('submit', e => {
        if (form.dataset.confirmed === '1') return;
        e.preventDefault();
        pendingForm = form;
        if (title) title.textContent = form.dataset.confirmTitle || 'Switch plan?';
        if (message) message.textContent = form.dataset.confirmMessage || 'This will update your Stripe subscription.';
        if (confirm) {
          confirm.disabled = false;
          confirm.textContent = form.dataset.confirmAction || 'Switch';
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

  // Credential tabs (Anthropic/OpenAI integrations).
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

  function setSettingsPanelDisabled(panel, disabled) {
    panel.querySelectorAll('input, select, textarea, button:not([data-settings-tab])').forEach(el => {
      el.disabled = disabled;
    });
  }
  document.querySelectorAll('[data-settings-tabs]').forEach(scope => {
    scope.querySelectorAll('[data-settings-panel]').forEach(panel => {
      setSettingsPanelDisabled(panel, !panel.classList.contains('active'));
    });
    scope.querySelectorAll('[data-settings-tab]').forEach(btn => {
      btn.addEventListener('click', () => {
        const tab = btn.dataset.settingsTab;
        scope.querySelectorAll('[data-settings-tab]').forEach(t => {
          const active = t.dataset.settingsTab === tab;
          t.classList.toggle('active', active);
          t.setAttribute('aria-selected', active ? 'true' : 'false');
        });
        scope.querySelectorAll('[data-settings-panel]').forEach(panel => {
          const active = panel.dataset.settingsPanel === tab;
          panel.classList.toggle('active', active);
          setSettingsPanelDisabled(panel, !active);
        });
      });
    });
  });

  document.querySelectorAll('[data-skill-upload-drop]').forEach(zone => {
    const input = zone.querySelector('[data-skill-upload-input]');
    const nameEl = zone.querySelector('[data-skill-upload-name]');
    if (!input) return;

    function setFileName() {
      const file = input.files && input.files[0];
      if (nameEl) nameEl.textContent = file ? file.name : '';
    }

    input.addEventListener('change', setFileName);
    ['dragenter', 'dragover'].forEach(type => {
      zone.addEventListener(type, e => {
        e.preventDefault();
        if (!input.disabled) zone.classList.add('is-dragging');
      });
    });
    ['dragleave', 'dragend'].forEach(type => {
      zone.addEventListener(type, e => {
        e.preventDefault();
        zone.classList.remove('is-dragging');
      });
    });
    zone.addEventListener('drop', e => {
      e.preventDefault();
      zone.classList.remove('is-dragging');
      if (input.disabled || !e.dataTransfer || !e.dataTransfer.files.length) return;
      try {
        input.files = e.dataTransfer.files;
      } catch (_) {
        return;
      }
      input.dispatchEvent(new Event('change', { bubbles: true }));
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
