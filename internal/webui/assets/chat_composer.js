async function loadAgents() {
  agentOptionsLoaded = false;
  try {
    const res = await fetch('/api/agents', { headers: { 'Accept': 'application/json' } });
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
  const hasTurns = !!(detail && Array.isArray(detail.history) && detail.history.length);
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

function openModelPopover() {
  if (modelLocked) return;
  modelPopover.hidden = false;
  modelBtn.setAttribute('aria-expanded', 'true');
  closeToolsPopover();
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
agentSelectorBtn.addEventListener('click', e => {
  e.stopPropagation();
  if (agentPopover.hidden) openAgentPopover();
  else closeAgentPopover();
});
document.getElementById('agent-flyout-root').addEventListener('mouseenter', openAgentPopover);
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
  closeMetaDropdown();
});
document.addEventListener('keydown', e => {
  if (e.key === 'Escape') {
    closeToolsPopover();
    closeModelPopover();
    closeAgentPopover();
    closeMetaDropdown();
  }
});

