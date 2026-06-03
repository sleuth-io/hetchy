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

  // Jobs tab: create/edit jobs through the JSON API and keep the
  // rendered list authoritative by reloading after successful writes.
  (function () {
    const dlg = document.getElementById('modal-job-edit');
    const form = document.getElementById('job-edit-form');
    if (!dlg || !form) return;

    const field = name => form.querySelector('[data-job-field="' + name + '"]');
    const errorEl = form.querySelector('[data-job-error]');
    const titleEl = form.querySelector('[data-job-modal-title]');
    const schedulePresetEl = form.querySelector('[data-job-schedule-preset]');
    const cronRow = form.querySelector('[data-job-cron-row]');
    const additionalSelectEl = form.querySelector('[data-job-additional-select]');
    const additionalChipsEl = form.querySelector('[data-job-additional-chips]');
    const defaultCronSchedule = '0 * * * *';
    const advancedScheduleValue = 'advanced';
    let selectedAdditionalRepos = [];
    const presetCronValues = new Set([
      '0 * * * *',
      '0 */4 * * *',
      '0 */6 * * *',
      '0 */8 * * *',
      '0 */12 * * *',
      '0 9 * * *',
      '0 9 * * 1'
    ]);

    function showError(message) {
      if (!errorEl) return;
      errorEl.textContent = message || 'Job could not be saved.';
      errorEl.hidden = false;
    }

    function clearError() {
      if (!errorEl) return;
      errorEl.textContent = '';
      errorEl.hidden = true;
    }

    function splitLines(value) {
      return (value || '').split(/\r?\n|,/).map(s => s.trim()).filter(Boolean);
    }

    function setValue(name, value) {
      const el = field(name);
      if (!el) return;
      if (el.type === 'checkbox') {
        el.checked = value === true || value === '1';
      } else {
        el.value = value || '';
      }
    }

    function primaryRepoValue() {
      const primary = field('primary_repository');
      return primary ? (primary.value || '').trim() : '';
    }

    function syncAdditionalRepoHiddenField() {
      setValue('additional_repositories', selectedAdditionalRepos.join('\n'));
    }

    function renderAdditionalRepoChips() {
      syncAdditionalRepoHiddenField();
      const primary = primaryRepoValue();
      if (additionalChipsEl) {
        additionalChipsEl.textContent = '';
        selectedAdditionalRepos.forEach(slug => {
          const chip = document.createElement('span');
          chip.className = 'job-repo-chip';
          const label = document.createElement('span');
          label.textContent = slug;
          const remove = document.createElement('button');
          remove.type = 'button';
          remove.dataset.jobAdditionalRemove = slug;
          remove.setAttribute('aria-label', 'Remove ' + slug);
          remove.textContent = 'x';
          chip.appendChild(label);
          chip.appendChild(remove);
          additionalChipsEl.appendChild(chip);
        });
      }
      if (additionalSelectEl) {
        Array.from(additionalSelectEl.options).forEach(option => {
          if (!option.value) return;
          option.disabled = option.value === primary || selectedAdditionalRepos.includes(option.value);
        });
        additionalSelectEl.value = '';
      }
    }

    function setAdditionalRepos(repos) {
      const primary = primaryRepoValue();
      const seen = new Set();
      selectedAdditionalRepos = [];
      (repos || []).forEach(raw => {
        const slug = (raw || '').trim();
        if (!slug || slug === primary || seen.has(slug)) return;
        seen.add(slug);
        selectedAdditionalRepos.push(slug);
      });
      renderAdditionalRepoChips();
    }

    function addAdditionalRepo(slug) {
      setAdditionalRepos(selectedAdditionalRepos.concat(slug || ''));
    }

    function defaultTimezone() {
      try {
        const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
        return tz || 'UTC';
      } catch (_) {
        return 'UTC';
      }
    }

    function showAdvancedSchedule(show) {
      if (!cronRow) return;
      cronRow.hidden = !show;
    }

    function setScheduleValue(cron) {
      const value = (cron || '').trim() || defaultCronSchedule;
      setValue('cron_schedule', value);
      if (!schedulePresetEl) return;
      if (presetCronValues.has(value)) {
        schedulePresetEl.value = value;
        showAdvancedSchedule(false);
      } else {
        schedulePresetEl.value = advancedScheduleValue;
        showAdvancedSchedule(true);
      }
    }

    function applySchedulePreset() {
      if (!schedulePresetEl) return;
      if (schedulePresetEl.value === advancedScheduleValue) {
        showAdvancedSchedule(true);
        const cron = field('cron_schedule');
        if (cron) cron.focus();
        return;
      }
      setValue('cron_schedule', schedulePresetEl.value || defaultCronSchedule);
      showAdvancedSchedule(false);
    }

    function resetForm() {
      clearError();
      form.reset();
      setValue('id', '');
      setValue('timezone', defaultTimezone());
      setScheduleValue(defaultCronSchedule);
      setAdditionalRepos([]);
      if (titleEl) titleEl.textContent = 'Create job';
    }

    function fillFromCard(card) {
      clearError();
      setValue('id', card.dataset.jobId || '');
      setValue('name', card.dataset.jobName || '');
      setValue('agent_slug', card.dataset.jobAgent || '');
      setValue('primary_repository', card.dataset.jobPrimary || '');
      setValue('timezone', card.dataset.jobTimezone || 'UTC');
      setScheduleValue(card.dataset.jobCron || defaultCronSchedule);
      const definition = card.querySelector('[data-job-definition]');
      setValue('definition', definition ? definition.textContent.trim() : '');
      const additional = card.querySelector('[data-job-additional]');
      setAdditionalRepos(splitLines(additional ? additional.textContent.trim() : ''));
      if (titleEl) titleEl.textContent = 'Edit job';
    }

    function openDialog() {
      if (typeof dlg.showModal === 'function' && !dlg.open) dlg.showModal();
      const name = field('name');
      if (name) setTimeout(() => name.focus(), 0);
    }

    function payloadFromForm() {
      return {
        name: field('name') ? field('name').value : '',
        definition: field('definition') ? field('definition').value : '',
        agent_slug: field('agent_slug') ? field('agent_slug').value : '',
        primary_repository: field('primary_repository') ? field('primary_repository').value : '',
        additional_repositories: selectedAdditionalRepos.slice(),
        cron_schedule: field('cron_schedule') ? field('cron_schedule').value : '',
        timezone: field('timezone') ? field('timezone').value : 'UTC'
      };
    }

    if (schedulePresetEl) {
      schedulePresetEl.addEventListener('change', applySchedulePreset);
    }
    const primaryRepoEl = field('primary_repository');
    if (primaryRepoEl) {
      primaryRepoEl.addEventListener('change', () => setAdditionalRepos(selectedAdditionalRepos));
    }
    if (additionalSelectEl) {
      additionalSelectEl.addEventListener('change', () => {
        addAdditionalRepo(additionalSelectEl.value);
      });
    }
    if (additionalChipsEl) {
      additionalChipsEl.addEventListener('click', e => {
        const btn = e.target.closest('[data-job-additional-remove]');
        if (!btn) return;
        setAdditionalRepos(selectedAdditionalRepos.filter(slug => slug !== btn.dataset.jobAdditionalRemove));
      });
    }

    async function requestJSON(url, options) {
      const res = await fetch(url, Object.assign({
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin'
      }, options || {}));
      if (!res.ok) {
        let message = 'Request failed.';
        try {
          const body = await res.json();
          if (body && body.error) message = body.error;
        } catch (_) {}
        throw new Error(message);
      }
      if (res.status === 204) return null;
      return res.json();
    }

    document.querySelectorAll('[data-job-new]').forEach(btn => {
      btn.addEventListener('click', () => {
        resetForm();
        openDialog();
      });
    });

    document.querySelectorAll('[data-job-edit]').forEach(btn => {
      btn.addEventListener('click', () => {
        const card = document.getElementById('job-' + btn.dataset.jobEdit);
        if (!card) return;
        fillFromCard(card);
        openDialog();
      });
    });

    form.addEventListener('submit', async e => {
      e.preventDefault();
      clearError();
      const id = field('id') ? field('id').value : '';
      const save = form.querySelector('button[type="submit"]');
      if (save) save.disabled = true;
      try {
        const url = id ? '/api/v1/jobs/' + encodeURIComponent(id) : '/api/v1/jobs';
        const method = id ? 'PATCH' : 'POST';
        await requestJSON(url, { method, body: JSON.stringify(payloadFromForm()) });
        window.location.href = '/settings/org?tab=jobs&saved=job_saved';
      } catch (err) {
        showError(err.message);
      } finally {
        if (save) save.disabled = false;
      }
    });

    document.querySelectorAll('[data-job-toggle]').forEach(btn => {
      btn.addEventListener('click', async () => {
        btn.disabled = true;
        try {
          await requestJSON('/api/v1/jobs/' + encodeURIComponent(btn.dataset.jobToggle), {
            method: 'PATCH',
            body: JSON.stringify({ enabled: btn.dataset.jobToggleEnabled === '1' })
          });
          window.location.href = '/settings/org?tab=jobs&saved=job_saved';
        } catch (err) {
          showToast(err.message, true);
          btn.disabled = false;
        }
      });
    });

    document.querySelectorAll('[data-job-run]').forEach(btn => {
      btn.addEventListener('click', async () => {
        btn.disabled = true;
        try {
          await requestJSON('/api/v1/jobs/' + encodeURIComponent(btn.dataset.jobRun) + '/run', { method: 'POST' });
          window.location.href = '/settings/org?tab=jobs&saved=job_started';
        } catch (err) {
          showToast(err.message, true);
          btn.disabled = false;
        }
      });
    });

    document.querySelectorAll('[data-job-delete]').forEach(btn => {
      btn.addEventListener('click', () => {
        confirmJobAction('Delete job?', 'This removes the schedule and execution history for this job. Existing conversations remain.', async () => {
          await requestJSON('/api/v1/jobs/' + encodeURIComponent(btn.dataset.jobDelete), { method: 'DELETE' });
          window.location.href = '/settings/org?tab=jobs&saved=job_deleted';
        });
      });
    });

    function showToast(message, isError) {
      const toast = document.createElement('div');
      toast.className = 'repo-toast' + (isError ? ' repo-toast-err' : '');
      toast.textContent = message;
      document.body.appendChild(toast);
      setTimeout(() => toast.remove(), 4500);
    }

    function confirmJobAction(title, message, fn) {
      const confirmDlg = document.getElementById('integration-disconnect-dialog');
      const titleEl = document.getElementById('integration-disconnect-title');
      const messageEl = document.getElementById('integration-disconnect-message');
      const cancel = document.getElementById('integration-disconnect-cancel');
      const confirm = document.getElementById('integration-disconnect-confirm');
      if (!confirmDlg || !confirm) {
        fn().catch(err => showToast(err.message, true));
        return;
      }
      if (titleEl) titleEl.textContent = title;
      if (messageEl) messageEl.textContent = message;
      confirm.textContent = 'Delete';
      confirm.disabled = false;
      const cleanup = () => {
        confirm.removeEventListener('click', onConfirm);
        if (cancel) cancel.removeEventListener('click', onCancel);
      };
      const onCancel = () => {
        cleanup();
        confirmDlg.close();
      };
      const onConfirm = async () => {
        confirm.disabled = true;
        try {
          await fn();
        } catch (err) {
          showToast(err.message, true);
          confirm.disabled = false;
          cleanup();
          confirmDlg.close();
        }
      };
      confirm.addEventListener('click', onConfirm);
      if (cancel) cancel.addEventListener('click', onCancel);
      if (typeof confirmDlg.showModal === 'function') {
        confirmDlg.showModal();
      } else {
        onConfirm();
      }
    }
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

  // Long skill lists on an agent card stay collapsed to the first 10
  // chips. The toggle button references its list via aria-controls so
  // assistive tech can announce the expanded state correctly.
  document.querySelectorAll('[data-skill-chip-list-toggle]').forEach(btn => {
    btn.addEventListener('click', () => {
      const id = btn.getAttribute('aria-controls');
      const list = id ? document.getElementById(id) : btn.previousElementSibling;
      if (!list || !list.classList.contains('skill-chip-list-collapsible')) return;
      const collapsed = list.classList.toggle('is-collapsed');
      btn.textContent = collapsed
        ? (btn.dataset.expandLabel || 'Show all')
        : (btn.dataset.collapseLabel || 'Show less');
      btn.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
    });
  });
