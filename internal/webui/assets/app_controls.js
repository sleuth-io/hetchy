  function renderAgentSelects() {
    renderAgentPicker();
    renderModelPicker();
    renderRepoPicker();
    updateTaskToolsButton();
  }
  function repoSlug(repo) {
    if (!repo || !repo.owner || !repo.name) return '';
    return repo.owner + '/' + repo.name;
  }
  function repoChoicesForPicker() {
    const choices = [];
    const seen = new Set();
    const selected = compact(state.selectedTaskRepo, '');
    if (!selected) {
      choices.push({ slug: '', label: 'Choose repository', placeholder: true });
    } else {
      choices.push({ slug: selected, label: selected });
      seen.add(selected);
    }
    const needle = state.repoSearch.trim().toLowerCase();
    for (const repo of state.repos) {
      const slug = repoSlug(repo);
      if (!slug || seen.has(slug)) continue;
      if (needle && !slug.toLowerCase().includes(needle)) continue;
      seen.add(slug);
      choices.push({ slug, label: slug });
    }
    return choices;
  }
  function renderRepoPicker() {
    const label = byID('task-repo-label');
    if (label) label.textContent = compact(state.selectedTaskRepo, 'Choose repository');
    const btn = byID('task-repo-btn');
    if (btn) {
      const current = compact(state.selectedTaskRepo, 'Choose repository');
      btn.title = 'Repository: ' + current;
      btn.setAttribute('aria-label', 'Choose repository. Current: ' + current);
    }
    const options = byID('task-repo-options');
    if (!options) return;
    const choices = repoChoicesForPicker();
    options.innerHTML = choices.map(choice =>
      '<button class="repo-choice' + (choice.slug === state.selectedTaskRepo ? ' is-selected' : '') + '" type="button" role="option"'
      + ' data-repo-slug="' + esc(choice.slug) + '" aria-selected="' + (choice.slug === state.selectedTaskRepo ? 'true' : 'false') + '"'
      + (choice.placeholder ? ' disabled aria-disabled="true"' : '') + '>'
      + '<span class="repo-choice-name">' + esc(choice.label) + '</span>'
      + '</button>'
    ).join('');
    const serverResults = choices.filter(choice => choice.slug && choice.slug !== state.selectedTaskRepo).length;
    if (!state.reposLoaded) {
      options.insertAdjacentHTML('beforeend', '<div class="repo-empty">Loading repositories...</div>');
    } else if (serverResults === 0) {
      options.insertAdjacentHTML('beforeend',
        '<div class="repo-empty">' + esc(state.repoSearch.trim() ? 'No repositories match "' + state.repoSearch.trim() + '".' : 'No repositories available.') + '</div>'
      );
    }
  }
  function renderAgentPicker() {
    const label = byID('task-agent-label');
    if (label) label.textContent = state.selectedTaskAgent ? agentName(state.selectedTaskAgent) : 'No agent';
    const search = byID('task-agent-search');
    if (search && search.value !== state.agentSearch) search.value = state.agentSearch;
    const options = byID('task-agent-options');
    if (!options) return;
    const usedAgents = [];
    const catalogAgents = [];
    const seen = new Set();
    const needle = compact(state.agentSearch, '').trim().toLowerCase();
    const matchesAgentSearch = agent => {
      if (!needle) return true;
      const haystack = [
        agent && agent.slug,
        agent && agent.display_name,
        agent && agent.description
      ].map(value => compact(value, '').toLowerCase()).join(' ');
      return haystack.includes(needle);
    };
    const addAgent = (agent, target) => {
      const slug = compact(agent && agent.slug, '');
      if (!slug || seen.has(slug)) return;
      seen.add(slug);
      if (matchesAgentSearch(agent)) target.push(agent);
    };
    for (const agent of state.agents || []) {
      addAgent(agent, usedAgents);
    }
    if (Array.isArray(state.agentCatalog) && state.agentCatalog.length) {
      for (const agent of state.agentCatalog) {
        if (agent && agent.catalog_only) addAgent(agent, catalogAgents);
        else addAgent(agent, usedAgents);
      }
    }
    const renderChoice = agent =>
      '<button class="agent-choice' + ((agent.slug || '') === state.selectedTaskAgent ? ' is-selected' : '') + '" type="button" data-agent-slug="' + esc(agent.slug || '') + '">'
      + '<span class="agent-choice-name">' + esc(agent.display_name || agent.slug || 'No agent') + '</span>'
      + (agent.description ? '<span class="agent-choice-desc">' + esc(agent.description) + '</span>' : '')
      + '</button>';
    const html = [
      renderChoice({ slug: '', display_name: 'No agent', description: 'Use Hetchy without a specialized persona.' })
    ];
    html.push.apply(html, usedAgents.map(renderChoice));
    if (catalogAgents.length) {
      html.push('<div class="agent-choice-section" role="separator"><span>Built-in catalog</span></div>');
      html.push.apply(html, catalogAgents.map(renderChoice));
    }
    if (needle && usedAgents.length === 0 && catalogAgents.length === 0) {
      html.push('<div class="agent-empty">No agents match "' + esc(state.agentSearch.trim()) + '".</div>');
    }
    options.innerHTML = html.join('');
  }
  const modelProviderHeadings = { anthropic: 'Claude', openai: 'GPT' };
  function renderModelPicker() {
    const label = byID('task-model-label');
    if (label) label.textContent = modelLabel(state.selectedTaskModel);
    const options = byID('task-model-options');
    if (!options) return;
    const providers = new Set(modelOptions.map(model => model.provider || 'anthropic'));
    const showHeadings = providers.size > 1;
    let lastProvider = '';
    options.innerHTML = modelOptions.map(model => {
      const provider = model.provider || 'anthropic';
      const heading = showHeadings && provider !== lastProvider
        ? '<div class="model-provider-heading">' + esc(modelProviderHeadings[provider] || provider) + '</div>'
        : '';
      lastProvider = provider;
      return heading
        + '<button class="model-choice' + (model.value === state.selectedTaskModel ? ' is-selected' : '') + '" type="button" data-model-value="' + esc(model.value) + '">'
        + '<span class="model-choice-name">' + esc(model.label) + '</span>'
        + '<span class="model-choice-desc">' + esc(model.description || '') + '</span>'
        + (model.value === state.selectedTaskModel ? '<span class="model-check">&#10003;</span>' : '')
        + '</button>';
    }).join('');
    const btn = byID('task-model-btn');
    if (btn) {
      const current = modelLabel(state.selectedTaskModel);
      btn.title = 'Model: ' + current;
      btn.setAttribute('aria-label', 'Choose model. Current: ' + current);
    }
  }
  function updateTaskToolsButton() {
    document.querySelectorAll('#task-tools-popover .tools-checkbox-row').forEach(row => {
      const box = row.querySelector('input[type="checkbox"]');
      row.classList.toggle('is-checked', !!(box && box.checked));
    });
  }
  function closePopover(id, buttonID) {
    const popover = byID(id);
    const btn = byID(buttonID);
    if (popover) popover.hidden = true;
    if (btn) btn.setAttribute('aria-expanded', 'false');
  }
  function openOnlyPopover(id, buttonID) {
    closeRunActionMenu();
    [
      ['task-tools-popover', 'task-tools-btn'],
      ['task-repo-popover', 'task-repo-btn'],
      ['task-agent-popover', 'task-agent-btn'],
      ['task-model-popover', 'task-model-btn'],
      ['agent-menu', 'agent-menu-btn'],
      ['new-create-menu', 'new-create-toggle'],
      ['user-menu-dropdown', 'user-menu-btn'],
    ].forEach(pair => {
      if (pair[0] === id) return;
      closePopover(pair[0], pair[1]);
    });
    const popover = byID(id);
    const btn = byID(buttonID);
    if (popover) popover.hidden = false;
    if (btn) btn.setAttribute('aria-expanded', 'true');
  }
  function togglePopover(id, buttonID) {
    const popover = byID(id);
    if (!popover) return;
    if (popover.hidden) openOnlyPopover(id, buttonID);
    else closePopover(id, buttonID);
  }
  function closeRunActionMenu() {
    if (activeRunActionButton) {
      activeRunActionButton.setAttribute('aria-expanded', 'false');
      activeRunActionButton = null;
    }
    const menu = byID('run-action-menu');
    if (menu) menu.hidden = true;
    state.runActionConversationID = '';
    state.runActionTitle = '';
  }
  function openRunActionMenu(btn, opts) {
    if (!btn) return;
    if (!opts?.force && activeRunActionButton === btn && !byID('run-action-menu').hidden) {
      closeRunActionMenu();
      return;
    }
    closeRunActionMenu();
    [
      ['task-tools-popover', 'task-tools-btn'],
      ['task-repo-popover', 'task-repo-btn'],
      ['task-agent-popover', 'task-agent-btn'],
      ['task-model-popover', 'task-model-btn'],
      ['agent-menu', 'agent-menu-btn'],
      ['new-create-menu', 'new-create-toggle'],
      ['user-menu-dropdown', 'user-menu-btn'],
    ].forEach(pair => closePopover(pair[0], pair[1]));
    activeRunActionButton = btn;
    state.runActionConversationID = btn.dataset.runActions || '';
    state.runActionTitle = btn.dataset.runTitle || '';
    btn.setAttribute('aria-expanded', 'true');
    const menu = byID('run-action-menu');
    const rect = btn.getBoundingClientRect();
    menu.style.top = (rect.bottom + 5) + 'px';
    menu.style.left = '';
    menu.style.right = Math.max(8, window.innerWidth - rect.right) + 'px';
    menu.hidden = false;
  }
  function openRunRenameDialog(conversationID, currentTitle) {
    if (!conversationID) return;
    const input = byID('run-rename-input');
    input.value = currentTitle || '';
    input.dataset.conversationId = conversationID || '';
    openDialog('run-rename-dialog');
    requestAnimationFrame(() => {
      input.focus();
      input.select();
    });
  }
  function openRunDeleteDialog(conversationID) {
    if (!conversationID) return;
    const dialog = byID('run-delete-dialog');
    dialog.dataset.conversationId = conversationID || '';
    openDialog('run-delete-dialog');
  }
  function openRunStopDialog(conversationID) {
    if (!conversationID) return;
    const dialog = byID('run-stop-dialog');
    dialog.dataset.conversationId = conversationID || '';
    openDialog('run-stop-dialog');
  }
  function updateConversationTitle(conversationID, title) {
    const update = run => {
      if (run && run.conversation_id === conversationID) run.title = title;
    };
    if (Array.isArray(state.data.runs)) state.data.runs.forEach(update);
    if (Array.isArray(state.data.pull_requests)) {
      state.data.pull_requests.forEach(pr => {
        if (pr && pr.conversation_id === conversationID) pr.title = title;
      });
    }
    if (state.workSearch) {
      if (Array.isArray(state.workSearch.runs)) state.workSearch.runs.forEach(update);
      if (Array.isArray(state.workSearch.pull_requests)) {
        state.workSearch.pull_requests.forEach(pr => {
          if (pr && pr.conversation_id === conversationID) pr.title = title;
        });
      }
    }
    if (state.activeDetail && state.activeDetail.id === conversationID) {
      state.activeDetail.title = title;
      byID('chat-detail-title').textContent = title;
    }
  }
  function removeConversation(conversationID) {
    const notConversation = item => item && item.conversation_id !== conversationID;
    if (Array.isArray(state.data.runs)) state.data.runs = state.data.runs.filter(notConversation);
    if (Array.isArray(state.data.pull_requests)) state.data.pull_requests = state.data.pull_requests.filter(notConversation);
    if (state.workSearch) {
      if (Array.isArray(state.workSearch.runs)) state.workSearch.runs = state.workSearch.runs.filter(notConversation);
      if (Array.isArray(state.workSearch.pull_requests)) state.workSearch.pull_requests = state.workSearch.pull_requests.filter(notConversation);
    }
    if (state.activeChatID === conversationID) {
      closeActiveChat({ replace: true });
    }
    ensureSelection();
    syncRouteURL({ replace: true });
  }
  async function saveRunRename() {
    const input = byID('run-rename-input');
    const conversationID = input.dataset.conversationId || '';
    const title = input.value.trim();
    if (!conversationID || !title) return;
    let res;
    try {
      res = await fetch('/api/v1/conversations/' + encodeURIComponent(conversationID), {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title }),
      });
    } catch (err) {
      showToast('run-rename-network', 'Could not rename chat. Network error.', 'error');
      return;
    }
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      showToast('run-rename-error', text || 'Could not rename chat.', 'error', 5000);
      return;
    }
    closeDialog('run-rename-dialog');
    updateConversationTitle(conversationID, title);
    renderAll();
    showToast('run-renamed', 'Chat renamed.', 'success', 2200);
    pollSoon();
  }
  async function confirmRunDelete() {
    const dialog = byID('run-delete-dialog');
    const conversationID = dialog.dataset.conversationId || '';
    if (!conversationID) return;
    let res;
    try {
      res = await fetch('/api/v1/conversations/' + encodeURIComponent(conversationID), { method: 'DELETE' });
    } catch (err) {
      showToast('run-delete-network', 'Could not delete chat. Network error.', 'error');
      return;
    }
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      showToast('run-delete-error', text || 'Could not delete chat.', 'error', 6000);
      return;
    }
    closeDialog('run-delete-dialog');
    removeConversation(conversationID);
    renderAll();
    showToast('run-deleted', 'Chat deleted.', 'success', 2200);
    pollSoon();
  }
  async function confirmRunStop() {
    const dialog = byID('run-stop-dialog');
    const conversationID = dialog.dataset.conversationId || '';
    if (!conversationID) return;
    const btn = byID('run-stop-confirm');
    btn.disabled = true;
    btn.textContent = 'Stopping...';
    try {
      closeDialog('run-stop-dialog');
      await cancelConversation(conversationID);
    } finally {
      dialog.dataset.conversationId = '';
      btn.disabled = false;
      btn.textContent = 'Stop run';
    }
  }
  function updateResponsivePanelButtons() {
    const root = document.documentElement;
    const navBtn = byID('agent-nav-toggle');
    const prBtn = byID('pr-sidebar-toggle');
    if (navBtn) navBtn.setAttribute('aria-expanded', root.classList.contains('agent-nav-open') ? 'true' : 'false');
    if (prBtn) prBtn.setAttribute('aria-expanded', root.classList.contains('agent-pr-open') ? 'true' : 'false');
  }
  function closeResponsivePanels() {
    document.documentElement.classList.remove('agent-nav-open', 'agent-pr-open');
    updateResponsivePanelButtons();
  }
  function toggleResponsivePanel(panel) {
    const root = document.documentElement;
    if (panel === 'nav') {
      root.classList.toggle('agent-nav-open');
      root.classList.remove('agent-pr-open');
    } else if (panel === 'pr') {
      root.classList.toggle('agent-pr-open');
      root.classList.remove('agent-nav-open');
    }
    updateResponsivePanelButtons();
  }
  function syncResponsivePanels() {
    const root = document.documentElement;
    if (!window.matchMedia('(max-width: 820px)').matches) root.classList.remove('agent-nav-open');
    if (!window.matchMedia('(max-width: 1180px)').matches) root.classList.remove('agent-pr-open');
    if (!window.matchMedia('(max-width: 820px)').matches) closeChatMetaPanel();
    updateResponsivePanelButtons();
  }
  function updateChatMetaButton() {
    const dialog = byID('chat-detail-dialog');
    const btn = byID('chat-meta-toggle');
    if (!dialog || !btn) return;
    btn.setAttribute('aria-expanded', dialog.classList.contains('chat-meta-open') ? 'true' : 'false');
  }
  function closeChatMetaPanel() {
    const dialog = byID('chat-detail-dialog');
    const scrim = byID('chat-meta-scrim');
    if (dialog) dialog.classList.remove('chat-meta-open');
    if (scrim) scrim.hidden = true;
    updateChatMetaButton();
  }
  function toggleChatMetaPanel() {
    const dialog = byID('chat-detail-dialog');
    const scrim = byID('chat-meta-scrim');
    if (!dialog) return;
    const opening = !dialog.classList.contains('chat-meta-open');
    dialog.classList.toggle('chat-meta-open', opening);
    if (scrim) scrim.hidden = !opening;
    updateChatMetaButton();
  }

  function openDialog(id) {
    const dialog = byID(id);
    if (!dialog) return;
    if (dialog.showModal) dialog.showModal();
    else dialog.setAttribute('open', '');
  }
  function closeDialog(id) {
    const dialog = byID(id);
    if (!dialog) return;
    if (id === 'chat-detail-dialog') {
      closeChatMetaPanel();
      stopDetailPoll();
      updateFollowupRunState(false);
    }
    if (id === 'run-stop-dialog') {
      dialog.dataset.conversationId = '';
      const btn = byID('run-stop-confirm');
      if (btn) {
        btn.disabled = false;
        btn.textContent = 'Stop run';
      }
    }
    if (dialog.close && dialog.open) dialog.close();
    else dialog.removeAttribute('open');
  }
  function clearActiveChatState(opts) {
    const hadChat = !!state.activeChatID;
    state.activeChatID = '';
    state.routeChatID = '';
    state.activeDetail = null;
    if (hadChat && (!opts || opts.syncURL !== false)) {
      syncRouteURL({ replace: !!(opts && opts.replace) });
    }
  }
  function closeActiveChat(opts) {
    state.suppressChatCloseSync = true;
    closeDialog('chat-detail-dialog');
    state.suppressChatCloseSync = false;
    clearActiveChatState(opts);
  }

  function openNewTask() {
    byID('task-input').value = '';
    state.selectedTaskRepo = initialTaskRepoSlug();
    state.selectedTaskAgent = '';
    state.selectedTaskModel = readStoredModel();
    state.taskAttachments = [];
    renderAttachmentList('task');
    const autoMergeBox = byID('task-auto-merge');
    if (autoMergeBox) autoMergeBox.checked = readStoredAutoMerge();
    updateTaskToolsButton();
    if (state.mode === 'agent' && state.selectedID !== noAgentID) {
      state.selectedTaskAgent = state.selectedID;
    }
    state.repoSearch = '';
    state.agentSearch = '';
    if (byID('task-repo-search')) byID('task-repo-search').value = '';
    if (byID('task-agent-search')) byID('task-agent-search').value = '';
    renderAgentSelects();
    openDialog('new-task-dialog');
    byID('task-input').focus();
  }

  async function submitNewTask(event) {
    event.preventDefault();
    const text = byID('task-input').value.trim();
    if (!text) {
      showToast('new-task-empty', 'Enter a task before dispatching.', 'warn');
      return;
    }
    const targetAgent = compact(state.selectedTaskAgent, '');
    const conversationID = newConversationID();
    let payload;
    try {
      payload = await taskPayload(text, conversationID);
    } catch (e) {
      if (e && e.message === 'missing-repo') {
        showToast('new-task-repo', 'Choose a repository before starting a chat.', 'warn');
        return;
      }
      showToast('attachment-read', 'Could not read one of the selected files.', 'error');
      return;
    }
    closeDialog('new-task-dialog');
    showToast('task-started', 'Task dispatched.', 'success', 2200);
    state.selectedRunLimit = initialVisibleRuns;
    focusTaskAgentGroup(targetAgent);
    backgroundTurn('/api/v1/conversations', payload);
  }

  // Empty agent slug maps to noAgentID; ensureSelection won't evict this selection during poll propagation.
  function focusTaskAgentGroup(agentSlug) {
    const targetID = compact(agentSlug, '') || noAgentID;
    // Drop a stale chat detail and the status filter so the freshly dispatched task lands in view, even when the group is unchanged.
    // Suppress closeActiveChat's own URL sync: the selection hasn't moved yet, so it would record a stray entry for the old group before the switch below. A single syncRouteURL at the end captures the final route instead.
    closeActiveChat({ syncURL: false });
    state.statusFilter = 'all';
    if (state.mode === 'agent' && state.selectedID === targetID) {
      renderAll();
      syncRouteURL({ replace: true });
      return;
    }
    state.mode = 'agent';
    state.selectedID = targetID;
    setModeButtonState();
    resetScopedWorkSearch();
    renderAll();
    scheduleWorkSearch();
    syncRouteURL();
  }

  async function taskPayload(text, conversationID) {
    const repo = compact(state.selectedTaskRepo, '');
    const agent = compact(state.selectedTaskAgent, '');
    const model = compact(state.selectedTaskModel, 'opus');
    if (!repo) {
      throw new Error('missing-repo');
    }
    try { localStorage.setItem('hetchy.model', model); } catch (e) {}
    const payload = {
      text,
      conversation_id: conversationID,
      session_id: conversationID,
      model,
      validate: byID('task-validate').checked,
      review_code_before_push: byID('task-review').checked,
      action_pr_checks_for_done: byID('task-pr-checks').checked,
      auto_merge: byID('task-auto-merge').checked,
    };
    if (repo) payload.repository = repo;
    if (agent) payload.agent_slug = agent;
    payload.attachments = await attachmentPayloads('task');
    return payload;
  }

  function newConversationID() {
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
      return crypto.randomUUID();
    }
    return 'chat_' + Date.now().toString(36) + '_' + Math.random().toString(36).slice(2, 8);
  }

  async function backgroundTurn(path, payload) {
    pollSoon();
    let res;
    try {
      res = await fetch(path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
    } catch (e) {
      showToast('turn-network', 'Could not start the run. Network error.', 'error');
      pollSoon();
      return;
    }
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      showToast('turn-error', text || ('Run request failed: ' + res.status), 'error', 6000);
      pollSoon();
      return;
    }
    await consumeSSE(res, pollSoon).catch(() => pollSoon());
    pollSoon();
  }

  async function cancelConversation(conversationID) {
    conversationID = compact(conversationID, '');
    if (!conversationID || state.stopInFlightFor) return;
    state.stopInFlightFor = conversationID;
    updateFollowupRunState(conversationIsRunning(state.activeChatID));
    try {
      const res = await fetch('/api/v1/conversations/' + encodeURIComponent(conversationID) + '/cancel', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      });
      if (!res.ok) {
        const body = await res.text().catch(() => '');
        showToast('stop-run', body || ('Stop request failed: ' + res.status), 'error', 6000);
        return;
      }
      showToast('stop-run', 'Stop requested.', 'success', 1800);
      if (state.activeChatID === conversationID) {
        await loadChatDetail(conversationID, { quiet: true, preserveUI: true });
      }
    } catch (e) {
      showToast('stop-run', 'Could not stop the run. Network error.', 'error', 6000);
    } finally {
      state.stopInFlightFor = '';
      updateFollowupRunState(conversationIsRunning(state.activeChatID));
      pollSoon();
      scheduleDetailPoll(650);
    }
  }
