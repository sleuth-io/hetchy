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
