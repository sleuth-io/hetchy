// loadRepos fetches the first page of repositories the org's GitHub
// App installations grant access to. The endpoint accepts an optional
// `q=` substring so we re-fetch on each keystroke; orgs with thousands
// of repos never need to be paged into the browser. A stale-response
// guard via repoLoadToken means a slow earlier fetch can't clobber a
// newer query the user just typed.
async function loadRepos(query) {
  const token = ++repoLoadToken;
  const url = new URL('/api/v1/repositories', window.location.origin);
  if (query) url.searchParams.set('q', query);
  try {
    const res = await fetch(url.pathname + url.search, {
      headers: { 'Accept': 'application/json' },
    });
    if (!res.ok) throw new Error('repositories fetch failed: ' + res.status);
    const data = await res.json();
    if (token !== repoLoadToken) return;
    repoOptions = Array.isArray(data) ? data : [];
    repoOptionsLoaded = true;
  } catch (e) {
    if (token !== repoLoadToken) return;
    repoOptions = [];
    repoOptionsLoaded = true;
  }
  populateRepoPicker();
}

function persistSelectedRepo() {
  try { localStorage.setItem(repoStorageKey, selectedRepoSlug); } catch (e) {}
}

function parseRepoSlug(slug) {
  const trimmed = (slug || '').trim();
  if (!trimmed) return null;
  const parts = trimmed.split('/');
  if (parts.length !== 2 || !parts[0] || !parts[1]) return null;
  return { owner: parts[0], name: parts[1] };
}

function selectedRepoLabel() {
  return selectedRepoSlug || 'Default';
}

function updateRepoButton() {
  if (!repoSelectorValueEl || !repoSelectorBtn) return;
  const label = selectedRepoLabel();
  repoSelectorValueEl.textContent = label;
  repoSelectorBtn.title = 'Repository: ' + label;
  repoSelectorBtn.setAttribute('aria-label', 'Choose repository. Current: ' + label);
  repoSelectorBtn.classList.toggle('has-selection', !!selectedRepoSlug);
}

function chooseRepo(slug) {
  selectedRepoSlug = (slug || '').trim();
  persistSelectedRepo();
  populateRepoPicker();
  updateRepoButton();
  updateMutablePendingRepoMetadata();
  closeRepoPopover();
  inp.focus();
}

// repoChoicesForPicker merges the most-recent server page with the
// currently selected slug so the dropdown always shows that slug as
// "selected" even when it isn't on the first 20 — important for an org
// with thousands of repos where the chosen repo was found via search.
// The currently selected repo always renders (even when filtered out
// by the active search) so the picker never claims an empty state
// while a selection is in effect.
function repoChoicesForPicker() {
  const choices = [];
  const seen = new Set();
  // The "default" / clear-selection row pins to the top and is the
  // only way to revert to the org default without typing.
  choices.push({ owner: '', name: '', label: 'Use org default', isDefault: true });
  if (selectedRepoSlug) {
    const parts = parseRepoSlug(selectedRepoSlug);
    if (parts) {
      const slug = parts.owner + '/' + parts.name;
      seen.add(slug);
      choices.push({ owner: parts.owner, name: parts.name, label: slug });
    }
  }
  for (const repo of repoOptions) {
    if (!repo || !repo.owner || !repo.name) continue;
    const slug = repo.owner + '/' + repo.name;
    if (seen.has(slug)) continue;
    if (!matchesSearch(slug)) continue;
    seen.add(slug);
    choices.push({ owner: repo.owner, name: repo.name, label: slug });
  }
  return choices;
}

function matchesSearch(slug) {
  const needle = repoSearchQuery.trim().toLowerCase();
  if (!needle) return true;
  return slug.toLowerCase().includes(needle);
}

function populateRepoPicker() {
  if (!repoOptionsEl) return;
  repoOptionsEl.innerHTML = '';
  const choices = repoChoicesForPicker();
  for (const choice of choices) {
    const slug = choice.owner && choice.name ? choice.owner + '/' + choice.name : '';
    const item = document.createElement('button');
    item.type = 'button';
    item.className = 'repo-choice' + (slug === selectedRepoSlug ? ' is-selected' : '');
    item.dataset.repoSlug = slug;
    const name = document.createElement('span');
    name.className = 'repo-choice-name';
    name.textContent = choice.label;
    item.appendChild(name);
    item.addEventListener('click', () => chooseRepo(slug));
    repoOptionsEl.appendChild(item);
  }
  // Loading / empty placeholder row appended after the choices so a
  // user with a stored selection still sees it pinned to the top
  // while the server response is in flight or the search has no
  // matches. Count only "real" server-side results — the "Use org
  // default" row and the pinned selected repo row both render
  // unconditionally and shouldn't suppress the empty-state hint
  // when the search truly returned nothing else.
  if (!repoOptionsLoaded) {
    const loading = document.createElement('div');
    loading.className = 'repo-empty';
    loading.textContent = 'Loading repositories…';
    repoOptionsEl.appendChild(loading);
    return;
  }
  const serverResults = choices.filter(c => {
    if (!c.owner || !c.name) return false;
    return (c.owner + '/' + c.name) !== selectedRepoSlug;
  }).length;
  if (serverResults === 0) {
    const empty = document.createElement('div');
    empty.className = 'repo-empty';
    empty.textContent = repoSearchQuery.trim()
      ? 'No other repositories match “' + repoSearchQuery + '”.'
      : 'No repositories available. Install the GitHub App at /settings/org → Integrations.';
    repoOptionsEl.appendChild(empty);
  }
  updateRepoButton();
}

function applyConversationRepo(detail) {
  if (detail) {
    const owner = (detail.github_owner || '').trim();
    const name = (detail.github_repo || '').trim();
    // Display the conversation's pinned repo (or empty when none yet)
    // but don't update localStorage — the per-user "preferred default
    // repo" should survive a sidebar click into a chat that targets a
    // different repo, just like the Agent picker keeps its stored slug
    // when viewing a conversation with a different agent.
    selectedRepoSlug = owner && name ? owner + '/' + name : '';
  } else {
    selectedRepoSlug = readStoredRepoSlug();
  }
  populateRepoPicker();
  updateRepoButton();
}

async function loadAgents() {
  agentOptionsLoaded = false;
  try {
    const res = await fetch('/api/v1/agents', { headers: { 'Accept': 'application/json' } });
    if (!res.ok) throw new Error('agents fetch failed: ' + res.status);
    agentOptions = await res.json();
    agentOptionsLoaded = true;
  } catch (e) {
    agentOptions = [];
  }
  if (agentOptionsLoaded) reconcileSelectedAgentWithOptions();
  populateAgentPicker();
}

function persistSelectedAgent() {
  try { localStorage.setItem(agentStorageKey, selectedAgentSlug); } catch (e) {}
}

function agentSlugIsKnown(slug) {
  return !slug || agentOptions.some(agent => agent.slug === slug);
}

function reconcileSelectedAgentWithOptions() {
  if (!agentOptionsLoaded) return;
  if (agentSlugIsKnown(selectedAgentSlug)) return;
  selectedAgentSlug = '';
  persistSelectedAgent();
}

function selectedAgentName() {
  if (!selectedAgentSlug) return 'none';
  const found = agentOptions.find(agent => agent.slug === selectedAgentSlug);
  return found ? (found.display_name || found.slug) : selectedAgentSlug;
}

function updateToolsButton() {
  const agentLabel = selectedAgentName();
  const validates = validateBox ? validateBox.checked : true;
  const reviewsBeforePush = reviewBeforePushBox ? reviewBeforePushBox.checked : true;
  const actionsPRChecks = actionPRChecksBox ? actionPRChecksBox.checked : true;
  agentSelectorValueEl.textContent = agentLabel === 'none' ? 'No agent' : agentLabel;
  toolsBtn.title = 'Agent: ' + agentLabel + '; validation ' + (validates ? 'on' : 'off') + '; code review ' + (reviewsBeforePush ? 'on' : 'off') + '; PR checks ' + (actionsPRChecks ? 'on' : 'off');
  toolsBtn.setAttribute('aria-label', 'Composer options. Agent: ' + agentLabel + '. Validation ' + (validates ? 'on' : 'off') + '. Code review ' + (reviewsBeforePush ? 'on' : 'off') + '. PR checks ' + (actionsPRChecks ? 'on' : 'off') + '.');
  toolsBtn.classList.toggle('has-agent', !!selectedAgentSlug);
  qualityOptionRows.forEach(row => {
    const box = row.querySelector('input[type="checkbox"]');
    const checked = box ? box.checked : false;
    row.classList.toggle('is-checked', checked);
    row.setAttribute('aria-checked', String(checked));
  });
}

function taskOptionValue(detail, key) {
  const opts = detail && detail.task_options;
  if (opts && Object.prototype.hasOwnProperty.call(opts, key) && typeof opts[key] === 'boolean') {
    return opts[key];
  }
  return true;
}

function currentTaskOptions() {
  return {
    [taskOptionKeys.validate]: validateBox ? validateBox.checked : true,
    [taskOptionKeys.reviewBeforePush]: reviewBeforePushBox ? reviewBeforePushBox.checked : true,
    [taskOptionKeys.actionPRChecks]: actionPRChecksBox ? actionPRChecksBox.checked : true,
  };
}

function formatAttachmentSize(bytes) {
  const n = Number(bytes) || 0;
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(n < 10 * 1024 ? 1 : 0) + ' KB';
  return (n / (1024 * 1024)).toFixed(n < 10 * 1024 * 1024 ? 1 : 0) + ' MB';
}

function attachmentDisplayName(file) {
  return (file && (file.filename || file.name)) || 'attachment';
}

function renderPendingAttachments() {
  if (!attachmentListEl) return;
  attachmentListEl.innerHTML = '';
  if (pendingAttachments.length === 0) {
    attachmentListEl.hidden = true;
    return;
  }
  attachmentListEl.hidden = false;
  pendingAttachments.forEach((file, index) => {
    const chip = document.createElement('div');
    chip.className = 'attachment-chip';

    const name = document.createElement('span');
    name.className = 'attachment-chip-name';
    name.textContent = attachmentDisplayName(file);
    chip.appendChild(name);

    const size = document.createElement('span');
    size.className = 'attachment-chip-size';
    size.textContent = formatAttachmentSize(file.size || 0);
    chip.appendChild(size);

    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'attachment-chip-remove';
    remove.setAttribute('aria-label', 'Remove ' + attachmentDisplayName(file));
    remove.textContent = '×';
    remove.addEventListener('click', () => {
      pendingAttachments.splice(index, 1);
      renderPendingAttachments();
      inp.focus();
    });
    chip.appendChild(remove);

    attachmentListEl.appendChild(chip);
  });
}

function addPendingFiles(files) {
  const incoming = Array.from(files || []);
  if (incoming.length === 0) return;
  for (const file of incoming) {
    if (pendingAttachments.length >= maxPromptAttachments) {
      showToast('attachment-limit', 'You can attach up to ' + maxPromptAttachments + ' files.', 'warn', 3500);
      break;
    }
    if (file.size > maxPromptAttachmentBytes) {
      showToast('attachment-size', attachmentDisplayName(file) + ' is larger than 10 MB.', 'warn', 4500);
      continue;
    }
    pendingAttachments.push(file);
  }
  renderPendingAttachments();
}

function applyConversationTaskOptions(detail) {
  if (validateBox) validateBox.checked = taskOptionValue(detail, taskOptionKeys.validate);
  if (reviewBeforePushBox) reviewBeforePushBox.checked = taskOptionValue(detail, taskOptionKeys.reviewBeforePush);
  if (actionPRChecksBox) actionPRChecksBox.checked = taskOptionValue(detail, taskOptionKeys.actionPRChecks);
  updateToolsButton();
}

function chooseAgent(slug) {
  selectedAgentSlug = slug || '';
  persistSelectedAgent();
  populateAgentPicker();
  updateToolsButton();
  updateMutablePendingAgentMetadata();
  closeAgentPopover();
  closeToolsPopover();
  inp.focus();
}

function populateAgentPicker() {
  if (!agentOptionsEl) return;
  agentOptionsEl.innerHTML = '';
  const choices = [{
    slug: '',
    display_name: 'No agent',
    description: 'Use Hetchy without a specialized persona.'
  }, ...agentOptions];
  for (const agent of choices) {
    const item = document.createElement('button');
    item.type = 'button';
    item.className = 'agent-choice' + (agent.slug === selectedAgentSlug ? ' is-selected' : '');
    item.dataset.agentSlug = agent.slug;

    const name = document.createElement('span');
    name.className = 'agent-choice-name';
    name.textContent = agent.display_name || agent.slug;
    item.appendChild(name);

    if (agent.description) {
      const desc = document.createElement('span');
      desc.className = 'agent-choice-desc';
      desc.textContent = agent.description;
      item.appendChild(desc);
    }

    item.addEventListener('click', () => chooseAgent(agent.slug));
    agentOptionsEl.appendChild(item);
  }
  updateToolsButton();
}

function modelLabelFor(value) {
  const found = modelOptions.find(model => model.value === value);
  return found ? found.label : 'Opus';
}

function selectedModelLabel() {
  return modelLabelFor(selectedModel);
}

function updateModelButton() {
  const label = selectedModelLabel();
  modelLabelEl.textContent = label;
  const suffix = modelLocked ? ' (set for this chat)' : '';
  modelBtn.title = 'Model: ' + label + suffix;
  modelBtn.setAttribute('aria-label', 'Model: ' + label + suffix);
}

function setModelPickerLocked(locked) {
  modelLocked = !!locked;
  modelBtn.disabled = modelLocked;
  if (modelLocked) closeModelPopover();
  updateModelButton();
}

function applyConversationModel(detail) {
  const hasTurns = !!(detail && Array.isArray(detail.turns) && detail.turns.length);
  if (detail && modelOptions.some(model => model.value === detail.model)) {
    selectedModel = detail.model;
    populateModelPicker();
  }
  setModelPickerLocked(hasTurns);
}

function applyConversationAgent(detail) {
  selectedAgentSlug = detail ? (detail.agent_slug || '') : readStoredAgentSlug();
  if (!detail) reconcileSelectedAgentWithOptions();
  populateAgentPicker();
  updateToolsButton();
}

function chooseModel(value) {
  if (modelLocked) return;
  if (!modelOptions.some(model => model.value === value)) return;
  selectedModel = value;
  try { localStorage.setItem(modelStorageKey, selectedModel); } catch (e) {}
  populateModelPicker();
  updateModelButton();
  closeModelPopover();
  inp.focus();
}

function populateModelPicker() {
  if (!modelOptionsEl) return;
  modelOptionsEl.innerHTML = '';
  for (const model of modelOptions) {
    const item = document.createElement('button');
    item.type = 'button';
    item.className = 'model-choice' + (model.value === selectedModel ? ' is-selected' : '');

    const name = document.createElement('span');
    name.className = 'model-choice-name';
    name.textContent = model.label;
    item.appendChild(name);

    const desc = document.createElement('span');
    desc.className = 'model-choice-desc';
    desc.textContent = model.description;
    item.appendChild(desc);

    if (model.value === selectedModel) {
      const check = document.createElement('span');
      check.className = 'model-check';
      check.textContent = '✓';
      item.appendChild(check);
    }

    item.addEventListener('click', () => chooseModel(model.value));
    modelOptionsEl.appendChild(item);
  }
  updateModelButton();
}

function openToolsPopover() {
  toolsPopover.hidden = false;
  toolsBtn.setAttribute('aria-expanded', 'true');
  closeModelPopover();
  closeRepoPopover();
}

function closeToolsPopover() {
  toolsPopover.hidden = true;
  toolsBtn.setAttribute('aria-expanded', 'false');
  closeAgentPopover();
}

function openAgentPopover() {
  agentPopover.hidden = false;
  agentSelectorBtn.setAttribute('aria-expanded', 'true');
}

function closeAgentPopover() {
  agentPopover.hidden = true;
  agentSelectorBtn.setAttribute('aria-expanded', 'false');
}

function openRepoPopover() {
  if (!repoPopover || !repoSelectorBtn) return;
  repoPopover.hidden = false;
  repoSelectorBtn.setAttribute('aria-expanded', 'true');
  closeToolsPopover();
  closeModelPopover();
  // Focusing the search box on open turns "open the menu and start
  // typing" into one continuous action — matches what users expect of
  // a command-palette style picker. Selecting any existing value lets
  // a quick retype overwrite without a manual clear.
  if (repoSearchEl) {
    requestAnimationFrame(() => {
      repoSearchEl.focus();
      repoSearchEl.select();
    });
  }
}

function closeRepoPopover() {
  if (!repoPopover || !repoSelectorBtn) return;
  repoPopover.hidden = true;
  repoSelectorBtn.setAttribute('aria-expanded', 'false');
}

function openModelPopover() {
  if (modelLocked) return;
  modelPopover.hidden = false;
  modelBtn.setAttribute('aria-expanded', 'true');
  closeToolsPopover();
  closeRepoPopover();
}

function closeModelPopover() {
  modelPopover.hidden = true;
  modelBtn.setAttribute('aria-expanded', 'false');
}

toolsBtn.addEventListener('click', e => {
  e.stopPropagation();
  if (toolsPopover.hidden) openToolsPopover();
  else closeToolsPopover();
});
toolsPopover.addEventListener('click', e => e.stopPropagation());
if (attachFilesBtn && attachmentInput) {
  attachFilesBtn.addEventListener('mouseenter', closeAgentPopover);
  attachFilesBtn.addEventListener('click', e => {
    e.preventDefault();
    e.stopPropagation();
    attachmentInput.click();
  });
  attachmentInput.addEventListener('change', () => {
    addPendingFiles(attachmentInput.files);
    attachmentInput.value = '';
    closeToolsPopover();
    inp.focus();
  });
}
agentSelectorBtn.addEventListener('click', e => {
  e.stopPropagation();
  if (agentPopover.hidden) openAgentPopover();
  else closeAgentPopover();
});
document.getElementById('agent-flyout-root').addEventListener('mouseenter', openAgentPopover);
if (repoSelectorBtn) {
  repoSelectorBtn.addEventListener('click', e => {
    e.stopPropagation();
    if (repoPopover.hidden) openRepoPopover();
    else closeRepoPopover();
  });
}
if (repoSearchEl) {
  // Re-render the local list immediately so the UI feels responsive,
  // then fire off the server query for terms outside the cached 20.
  // Debounce the network call so each keystroke doesn't trigger its
  // own ListGithubReposByOrg scan; the trailing edge fires once the
  // user pauses typing.
  let repoSearchDebounce = null;
  let lastFetchedQuery = '';
  repoSearchEl.addEventListener('input', () => {
    repoSearchQuery = repoSearchEl.value;
    populateRepoPicker();
    if (repoSearchDebounce) clearTimeout(repoSearchDebounce);
    repoSearchDebounce = setTimeout(() => {
      const trimmed = repoSearchQuery.trim();
      if (trimmed === lastFetchedQuery) return;
      lastFetchedQuery = trimmed;
      loadRepos(trimmed);
    }, 180);
  });
  repoSearchEl.addEventListener('keydown', e => {
    if (e.key === 'Escape') {
      // Escape on a top-level chip popover should return focus to the
      // chip so keyboard users don't lose their place in the composer.
      e.preventDefault();
      closeRepoPopover();
      if (repoSelectorBtn) repoSelectorBtn.focus();
      return;
    }
    if (e.key === 'Enter') {
      // Enter picks the top non-default match so a quick type-and-go
      // doesn't require reaching for the mouse. The "Use org default"
      // row sits at index 0 with empty owner; the first actual repo
      // row is the first .repo-choice with a non-empty data-repo-slug.
      e.preventDefault();
      const first = repoOptionsEl.querySelector('.repo-choice[data-repo-slug]:not([data-repo-slug=""])');
      if (first) chooseRepo(first.dataset.repoSlug);
    }
  });
  repoSearchEl.addEventListener('click', e => e.stopPropagation());
}
if (repoPopover) {
  repoPopover.addEventListener('click', e => e.stopPropagation());
}
qualityOptionBoxes.forEach(box => box.addEventListener('change', updateToolsButton));
qualityOptionRows.forEach(row => row.addEventListener('mouseenter', closeAgentPopover));
helpIcons.forEach(icon => {
  icon.addEventListener('click', e => {
    e.preventDefault();
    e.stopPropagation();
  });
});
modelBtn.addEventListener('click', e => {
  e.stopPropagation();
  if (modelPopover.hidden) openModelPopover();
  else closeModelPopover();
});
modelPopover.addEventListener('click', e => e.stopPropagation());
document.addEventListener('click', () => {
  closeToolsPopover();
  closeModelPopover();
  closeAgentPopover();
  closeRepoPopover();
  closeMetaDropdown();
});
document.addEventListener('keydown', e => {
  if (e.key === 'Escape') {
    closeToolsPopover();
    closeModelPopover();
    closeAgentPopover();
    closeRepoPopover();
    closeMetaDropdown();
  }
});
