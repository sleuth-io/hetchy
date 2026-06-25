  function renderPendingFollowup(parent, item) {
    appendUserMessage(parent, item.text);
    const box = document.createElement('div');
    box.className = 'chat-block pending-followup';
    box.innerHTML = '<div class="chat-block-title">Follow-up queued</div>'
      + '<div class="chat-block-body">Hetchy will resume this conversation with the existing context.</div>';
    parent.appendChild(box);
  }

  function handleFollowupAction() {
    if (conversationIsRunning(state.activeChatID)) {
      openRunStopDialog(state.activeChatID);
      return;
    }
    sendFollowup();
  }

  async function sendFollowup() {
    const detail = state.activeDetail;
    if (conversationIsRunning(detail && detail.id)) return;
    const text = byID('followup-input').value.trim();
    if (!detail || !detail.id || !text) return;
    let attachments;
    try {
      attachments = await attachmentPayloads('followup');
    } catch (e) {
      showToast('attachment-read', 'Could not read one of the selected files.', 'error');
      return;
    }
    byID('followup-input').value = '';
    addPendingFollowup(detail.id, text);
    detail.status = 'running';
    renderChatDetail(detail);
    scheduleDetailPoll(650);
    const payload = {
      text,
      conversation_id: detail.id,
      session_id: detail.id,
      model: detail.model || 'opus',
      task_options: detail.task_options || {},
      attachments,
    };
    if (detail.agent_slug) payload.agent_slug = detail.agent_slug;
    if (detail.github_owner && detail.github_repo) payload.repository = detail.github_owner + '/' + detail.github_repo;
    state.followupAttachments = [];
    renderAttachmentList('followup');
    backgroundTurn('/api/v1/conversations/' + encodeURIComponent(detail.id) + '/turns', payload);
  }

  function attachmentsFor(kind) {
    return kind === 'followup' ? state.followupAttachments : state.taskAttachments;
  }
  function setAttachments(kind, files) {
    if (kind === 'followup') state.followupAttachments = files;
    else state.taskAttachments = files;
  }
  function inputForAttachments(kind) {
    return byID(kind === 'followup' ? 'followup-attachment-input' : 'task-attachment-input');
  }
  function listForAttachments(kind) {
    return byID(kind === 'followup' ? 'followup-attachment-list' : 'task-attachment-list');
  }
  function addAttachments(kind, files) {
    const current = attachmentsFor(kind).slice();
    for (const file of files) {
      if (current.length >= maxPromptAttachments) {
        showToast('attachment-limit', 'Maximum of 5 attachments per prompt.', 'warn');
        break;
      }
      if (file.size > maxPromptAttachmentBytes) {
        showToast('attachment-size', file.name + ' is larger than 10 MB.', 'warn');
        continue;
      }
      current.push(file);
    }
    setAttachments(kind, current);
    renderAttachmentList(kind);
  }
  function renderAttachmentList(kind) {
    const list = listForAttachments(kind);
    const files = attachmentsFor(kind);
    if (!list) return;
    list.hidden = files.length === 0;
    list.innerHTML = files.map((file, index) =>
      '<span class="attachment-chip">'
      + '<span class="attachment-chip-name">' + esc(file.name || 'attachment') + '</span>'
      + '<button class="attachment-remove" type="button" data-remove-attachment="' + esc(kind) + '" data-attachment-index="' + index + '" aria-label="Remove attachment">x</button>'
      + '</span>'
    ).join('');
  }
  async function attachmentPayloads(kind) {
    const files = attachmentsFor(kind);
    if (!files.length) return [];
    return Promise.all(files.map(fileToAttachmentPayload));
  }
  function fileToAttachmentPayload(file) {
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => {
        const dataURL = String(reader.result || '');
        const comma = dataURL.indexOf(',');
        resolve({
          filename: file.name || 'attachment',
          content_type: file.type || 'application/octet-stream',
          data_base64: comma >= 0 ? dataURL.slice(comma + 1) : dataURL,
          source: 'web',
        });
      };
      reader.onerror = () => reject(reader.error || new Error('read attachment failed'));
      reader.readAsDataURL(file);
    });
  }

  function bindEvents() {
    byID('mode-agent').addEventListener('click', () => {
      state.mode = 'agent';
      state.selectedID = '';
      resetScopedWorkSearch();
      setModeButtonState();
      ensureSelection();
      renderAll();
      scheduleWorkSearch();
      syncRouteURL();
    });
    byID('mode-user').addEventListener('click', () => {
      state.mode = 'user';
      state.selectedID = '';
      resetScopedWorkSearch();
      setModeButtonState();
      ensureSelection();
      renderAll();
      scheduleWorkSearch();
      syncRouteURL();
    });
    byID('group-list').addEventListener('click', e => {
      const btn = e.target.closest('[data-group-id]');
      if (!btn) return;
      state.selectedID = btn.dataset.groupId;
      resetScopedWorkSearch();
      renderAll();
      scheduleWorkSearch();
      syncRouteURL();
      if (window.matchMedia('(max-width: 820px)').matches) closeResponsivePanels();
    });
    byID('nav-search').addEventListener('input', e => {
      state.navQuery = e.target.value.trim();
      renderGroups();
      if (state.mode === 'agent' && state.navQuery && !state.agentCatalogLoaded) {
        loadAgentCatalog().then(() => {
          if (state.mode === 'agent' && state.navQuery) renderGroups();
        });
      }
      syncRouteURL({ replace: true });
    });
    byID('work-search').addEventListener('input', e => {
      state.workQuery = e.target.value.trim();
      resetScopedWorkSearch();
      renderAll();
      scheduleWorkSearch();
      syncRouteURL({ replace: true });
    });
    document.querySelectorAll('[data-status-filter]').forEach(btn => {
      btn.addEventListener('click', () => {
        const next = btn.dataset.statusFilter || 'all';
        state.statusFilter = state.statusFilter === next && next !== 'all' ? 'all' : next;
        state.selectedRunLimit = initialVisibleRuns;
        renderAll();
        syncRouteURL();
      });
    });
    byID('show-more-btn').addEventListener('click', () => {
      state.selectedRunLimit += initialVisibleRuns;
      renderRuns();
    });
    byID('run-rename-action').addEventListener('click', e => {
      e.stopPropagation();
      const conversationID = state.runActionConversationID;
      const title = state.runActionTitle;
      closeRunActionMenu();
      openRunRenameDialog(conversationID, title);
    });
    byID('run-delete-action').addEventListener('click', e => {
      e.stopPropagation();
      const conversationID = state.runActionConversationID;
      closeRunActionMenu();
      openRunDeleteDialog(conversationID);
    });
    byID('run-rename-cancel').addEventListener('click', () => closeDialog('run-rename-dialog'));
    byID('run-rename-save').addEventListener('click', saveRunRename);
    byID('run-rename-input').addEventListener('keydown', e => {
      if (e.key === 'Enter') {
        e.preventDefault();
        saveRunRename();
      }
    });
    byID('run-delete-cancel').addEventListener('click', () => closeDialog('run-delete-dialog'));
    byID('run-delete-confirm').addEventListener('click', confirmRunDelete);
    setupDetailMetaMenu();
    byID('run-stop-cancel').addEventListener('click', () => closeDialog('run-stop-dialog'));
    byID('run-stop-confirm').addEventListener('click', confirmRunStop);
    byID('agent-nav-toggle').addEventListener('click', e => {
      e.stopPropagation();
      toggleResponsivePanel('nav');
    });
    byID('pr-sidebar-toggle').addEventListener('click', e => {
      e.stopPropagation();
      toggleResponsivePanel('pr');
    });
    byID('agent-overlay-scrim').addEventListener('click', closeResponsivePanels);
    window.addEventListener('resize', syncResponsivePanels);
    bindVisualViewport();
    document.addEventListener('keydown', e => {
      if (e.key === 'Escape') {
        closeResponsivePanels();
        closeChatMetaPanel();
        closeRunActionMenu();
      }
    });
    byID('chat-meta-toggle').addEventListener('click', e => {
      e.stopPropagation();
      toggleChatMetaPanel();
    });
    byID('chat-meta-scrim').addEventListener('click', closeChatMetaPanel);
    byID('chat-log').addEventListener('scroll', () => {
      state.detailStickToBottom = isDetailLogAtBottom(byID('chat-log'));
    }, { passive: true });
    byID('run-list').addEventListener('keydown', e => {
      if (e.key !== 'Enter' && e.key !== ' ') return;
      if (e.target.closest('[data-run-actions]')) return;
      const card = e.target.closest('[data-run-card]');
      if (!card) return;
      e.preventDefault();
      openChat(card.dataset.runCard);
    });
    byID('new-task-btn').addEventListener('click', openNewTask);
    byID('new-task-form').addEventListener('submit', submitNewTask);
    const newCreateToggle = byID('new-create-toggle');
    if (newCreateToggle) {
      newCreateToggle.addEventListener('click', e => {
        e.stopPropagation();
        togglePopover('new-create-menu', 'new-create-toggle');
      });
    }
    byID('agent-menu-btn').addEventListener('click', e => {
      e.stopPropagation();
      togglePopover('agent-menu', 'agent-menu-btn');
    });
    byID('task-tools-btn').addEventListener('click', e => {
      e.stopPropagation();
      togglePopover('task-tools-popover', 'task-tools-btn');
    });
    byID('task-repo-btn').addEventListener('click', e => {
      e.stopPropagation();
      togglePopover('task-repo-popover', 'task-repo-btn');
      if (!byID('task-repo-popover').hidden) {
        requestAnimationFrame(() => {
          byID('task-repo-search').focus();
          byID('task-repo-search').select();
        });
      }
    });
    byID('task-agent-btn').addEventListener('click', e => {
      e.stopPropagation();
      togglePopover('task-agent-popover', 'task-agent-btn');
      if (!byID('task-agent-popover').hidden) {
        requestAnimationFrame(() => {
          byID('task-agent-search').focus();
          byID('task-agent-search').select();
        });
      }
      if (!byID('task-agent-popover').hidden && !state.agentCatalogLoaded) {
        loadAgentCatalog().then(renderAgentPicker);
      }
    });
    byID('task-model-btn').addEventListener('click', e => {
      e.stopPropagation();
      togglePopover('task-model-popover', 'task-model-btn');
    });
    byID('task-attach-files-btn').addEventListener('click', e => {
      e.preventDefault();
      e.stopPropagation();
      inputForAttachments('task').click();
    });
    byID('task-tools-popover').addEventListener('change', e => {
      if (!e.target.matches('input[type="checkbox"]')) return;
      if (e.target.id === 'task-auto-merge') persistAutoMergeEnabled(e.target.checked);
      updateTaskToolsButton();
    });
    byID('task-repo-search').addEventListener('input', e => {
      state.repoSearch = e.target.value;
      renderRepoPicker();
      clearTimeout(byID('task-repo-search')._debounce);
      byID('task-repo-search')._debounce = setTimeout(() => loadRepos(state.repoSearch.trim()), 180);
    });
    byID('task-agent-search').addEventListener('input', e => {
      state.agentSearch = e.target.value;
      renderAgentPicker();
      if (state.agentSearch.trim() && !state.agentCatalogLoaded) {
        loadAgentCatalog().then(renderAgentPicker);
      }
    });
    byID('task-repo-search').addEventListener('keydown', e => {
      if (e.key === 'Enter') {
        e.preventDefault();
        const first = byID('task-repo-options').querySelector('.repo-choice[data-repo-slug]:not([data-repo-slug=""])');
        if (first) {
          chooseTaskRepo(first.dataset.repoSlug);
          closePopover('task-repo-popover', 'task-repo-btn');
          byID('task-input').focus();
        }
      } else if (e.key === 'Escape') {
        e.preventDefault();
        closePopover('task-repo-popover', 'task-repo-btn');
        byID('task-repo-btn').focus();
      }
    });
    byID('followup-send-btn').addEventListener('click', handleFollowupAction);
    byID('followup-input').addEventListener('keydown', e => {
      if (e.key === 'Enter' && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
        e.preventDefault();
        sendFollowup();
      }
    });
    document.addEventListener('click', e => {
      const stopRunBtn = e.target.closest('[data-stop-run]');
      if (stopRunBtn) {
        e.preventDefault();
        closeRunActionMenu();
        openRunStopDialog(stopRunBtn.dataset.stopRun);
        return;
      }
      const runActionBtn = e.target.closest('[data-run-actions]');
      if (runActionBtn) {
        e.preventDefault();
        openRunActionMenu(runActionBtn);
        return;
      }
      if (!e.target.closest('#run-action-menu')) closeRunActionMenu();
      const catalogToggle = e.target.closest('[data-agent-catalog-toggle]');
      if (catalogToggle) {
        e.preventDefault();
        state.agentCatalogExpanded = !state.agentCatalogExpanded;
        renderGroups();
        if (state.agentCatalogExpanded && !state.agentCatalogLoaded) {
          loadAgentCatalog().then(renderAll);
        }
        return;
      }
      const repoChoice = e.target.closest('[data-repo-slug]');
      if (repoChoice && repoChoice.closest('#task-repo-options')) {
        chooseTaskRepo(repoChoice.dataset.repoSlug);
        closePopover('task-repo-popover', 'task-repo-btn');
        byID('task-input').focus();
        return;
      }
      const agentChoice = e.target.closest('[data-agent-slug]');
      if (agentChoice && agentChoice.closest('#task-agent-options')) {
        state.selectedTaskAgent = agentChoice.dataset.agentSlug;
        closePopover('task-agent-popover', 'task-agent-btn');
        renderAgentPicker();
        byID('task-input').focus();
        return;
      }
      const modelChoice = e.target.closest('[data-model-value]');
      if (modelChoice && modelChoice.closest('#task-model-options')) {
        state.selectedTaskModel = modelChoice.dataset.modelValue;
        try { localStorage.setItem('hetchy.model', state.selectedTaskModel); } catch (err) {}
        closePopover('task-model-popover', 'task-model-btn');
        renderModelPicker();
        byID('task-input').focus();
        return;
      }
      const attachBtn = e.target.closest('[data-attach-target]');
      if (attachBtn) {
        const input = inputForAttachments(attachBtn.dataset.attachTarget);
        if (input) input.click();
        return;
      }
      const removeAttachment = e.target.closest('[data-remove-attachment]');
      if (removeAttachment) {
        const kind = removeAttachment.dataset.removeAttachment;
        const index = Number(removeAttachment.dataset.attachmentIndex);
        const files = attachmentsFor(kind).slice();
        if (Number.isInteger(index)) files.splice(index, 1);
        setAttachments(kind, files);
        renderAttachmentList(kind);
        return;
      }
      const closeBtn = e.target.closest('[data-close-dialog]');
      if (closeBtn) {
        if (closeBtn.dataset.closeDialog === 'chat-detail-dialog') {
          closeActiveChat();
        } else {
          closeDialog(closeBtn.dataset.closeDialog);
        }
        return;
      }
      const chat = e.target.closest('[data-open-chat]');
      if (chat) {
        e.preventDefault();
        openChat(chat.dataset.openChat);
        return;
      }
      const card = e.target.closest('[data-run-card]');
      if (card && !e.target.closest('a')) {
        openChat(card.dataset.runCard);
        return;
      }
      if (!e.target.closest('.meta-more-wrap')) closeDetailMetaDropdown();
      // The "New Job" item lives inside the split-button menu; close the
      // menu once chosen so it isn't left open behind the job modal.
      if (e.target.closest('#new-create-menu [data-job-new]')) {
        closePopover('new-create-menu', 'new-create-toggle');
        return;
      }
      if (!e.target.closest('.composer-popover') && !e.target.closest('.floating-menu')) {
        closePopover('task-tools-popover', 'task-tools-btn');
        closePopover('task-repo-popover', 'task-repo-btn');
        closePopover('task-agent-popover', 'task-agent-btn');
        closePopover('task-model-popover', 'task-model-btn');
        closePopover('agent-menu', 'agent-menu-btn');
        closePopover('new-create-menu', 'new-create-toggle');
      }
    });
    byID('user-menu-btn').addEventListener('click', () => {
      const menu = byID('user-menu-dropdown');
      const open = menu.hidden;
      menu.hidden = !open;
      byID('user-menu-btn').setAttribute('aria-expanded', open ? 'true' : 'false');
    });
    byID('chat-detail-dialog').addEventListener('close', () => {
      if (state.suppressChatCloseSync || !state.activeChatID) return;
      clearActiveChatState();
    });
    window.addEventListener('popstate', () => {
      applyRouteState();
      ensureSelection();
      renderAll();
      scheduleWorkSearch();
      syncResponsivePanels();
      openRouteChat();
    });
    inputForAttachments('task').addEventListener('change', e => {
      addAttachments('task', Array.from(e.target.files || []));
      e.target.value = '';
      closePopover('task-tools-popover', 'task-tools-btn');
      byID('task-input').focus();
    });
    inputForAttachments('followup').addEventListener('change', e => {
      addAttachments('followup', Array.from(e.target.files || []));
      e.target.value = '';
    });
    document.addEventListener('visibilitychange', () => {
      schedulePoll(document.hidden ? hiddenPollMs : 250);
      scheduleDetailPoll(document.hidden ? hiddenPollMs : 250);
    });
  }

  // Keep mobile dialogs sized to the visible viewport. On iOS the on-screen
  // keyboard shrinks window.visualViewport but leaves 100dvh unchanged, so a
  // centered full-height dialog would push its composer controls (repo, model,
  // send, etc.) underneath the keyboard. Mirroring the visual viewport into CSS
  // custom properties lets the dialog stylesheet anchor to the top and shrink.
  var visualViewportFrame = 0;
  function writeVisualViewportVars() {
    visualViewportFrame = 0;
    const vv = window.visualViewport;
    if (!vv) return;
    const root = document.documentElement;
    root.style.setProperty('--app-vvh', vv.height + 'px');
    root.style.setProperty('--app-vvt', vv.offsetTop + 'px');
  }
  function syncVisualViewport() {
    // Coalesce the resize/scroll bursts iOS fires while the keyboard animates so
    // we touch the custom properties at most once per frame.
    if (visualViewportFrame) return;
    visualViewportFrame = requestAnimationFrame(writeVisualViewportVars);
  }
  function bindVisualViewport() {
    const vv = window.visualViewport;
    if (!vv) return;
    vv.addEventListener('resize', syncVisualViewport);
    vv.addEventListener('scroll', syncVisualViewport);
    writeVisualViewportVars();
  }

  async function init() {
    bindEvents();
    applyRouteState();
    syncResponsivePanels();
    renderAgentSelects();
    await loadSupportData();
    await fetchAppData();
    scheduleWorkSearch();
  }

  init();
