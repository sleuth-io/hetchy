  async function consumeSSE(res, onEvent) {
    if (!res.body) return;
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = '';
    while (true) {
      const read = await reader.read();
      if (read.done) break;
      buf += decoder.decode(read.value, { stream: true });
      const parts = buf.split('\n\n');
      buf = parts.pop();
      for (const part of parts) {
        if (!part || part.startsWith(':')) continue;
        onEvent();
      }
    }
  }

  async function openChat(conversationID, opts) {
    state.activeChatID = conversationID;
    state.routeChatID = conversationID;
    state.detailStickToBottom = true;
    closeChatMetaPanel();
    stopDetailPoll();
    openDialog('chat-detail-dialog');
    if (!opts || opts.syncURL !== false) syncRouteURL();
    byID('chat-log').innerHTML = '<div class="empty">Loading...</div>';
    await loadChatDetail(conversationID);
    scheduleDetailPoll(250);
  }
  async function loadChatDetail(conversationID, opts) {
    try {
      const detail = await fetchJSON('/api/v1/conversations/' + encodeURIComponent(conversationID) + '?include=turns,attachments');
      if (state.activeChatID !== conversationID) return null;
      state.activeDetail = detail;
      renderChatDetail(detail, opts || {});
      return detail;
    } catch (e) {
      if (!opts || !opts.quiet) byID('chat-log').innerHTML = '<div class="empty">Could not load chat details.</div>';
      return null;
    }
  }

  function detailOpenStateKey(el, index) {
    if (el.dataset && el.dataset.blockId) return 'block:' + el.dataset.blockId;
    const title = el.querySelector('.blk-title')?.textContent || '';
    const startedAt = el.dataset?.startedAt || '';
    return 'idx:' + index + ':' + title + ':' + startedAt;
  }
  function isDetailLogAtBottom(log) {
    if (!log) return true;
    return log.scrollHeight - log.scrollTop - log.clientHeight <= detailScrollBottomThreshold;
  }
  function scrollDetailLogToBottom(log) {
    if (!log) return;
    log.scrollTop = log.scrollHeight;
    state.detailStickToBottom = true;
  }
  function captureDetailUIState(log) {
    const openStates = new Map();
    log.querySelectorAll('details.blk').forEach((el, index) => {
      openStates.set(detailOpenStateKey(el, index), !!el.open);
    });
    const wasPinned = isDetailLogAtBottom(log);
    state.detailStickToBottom = wasPinned;
    return {
      openStates,
      scrollTop: log.scrollTop,
      wasPinned,
    };
  }
  function restoreDetailScrollPosition(log, snapshot) {
    if (!snapshot) {
      scrollDetailLogToBottom(log);
      return;
    }
    if (snapshot.wasPinned) scrollDetailLogToBottom(log);
    else {
      log.scrollTop = Math.min(snapshot.scrollTop, log.scrollHeight);
      state.detailStickToBottom = isDetailLogAtBottom(log);
    }
  }
  function restoreDetailUIState(log, snapshot) {
    if (!snapshot) {
      scrollDetailLogToBottom(log);
      return;
    }
    log.querySelectorAll('details.blk').forEach((el, index) => {
      const key = detailOpenStateKey(el, index);
      if (snapshot.openStates.has(key)) el.open = snapshot.openStates.get(key);
    });
    restoreDetailScrollPosition(log, snapshot);
  }

  function renderChatDetail(detail, opts) {
    byID('chat-detail-title').textContent = detail.title || 'Chat details';
    byID('chat-detail-sub').textContent = [agentName(detail.agent_slug), detail.status || 'idle', detail.id].filter(Boolean).join(' - ');
    renderDetailMetadata(detail);
    const repo = detailRepoSlug(detail) || '-';
    byID('followup-repo-chip').textContent = repo;
    byID('followup-agent-chip').textContent = agentName(detail.agent_slug);
    byID('followup-model-chip').textContent = modelLabel(detail.model || 'opus');

    const turns = Array.isArray(detail.turns) ? detail.turns : [];
    const pending = visiblePendingFollowups(detail.id, turns);
    const log = byID('chat-log');
    const uiSnapshot = opts && opts.preserveUI ? captureDetailUIState(log) : null;
    const isRunning = detailIsRunning(detail) || runForConversation(detail.id)?.status === 'running';
    updateFollowupRunState(isRunning);
    if (!turns.length && !pending.length) {
      log.innerHTML = '<div class="empty">No turns yet.</div>';
      return;
    }
    log.innerHTML = '';
    for (let i = 0; i < turns.length; i++) {
      const turn = turns[i] || {};
      appendUserMessage(log, turn.message || '');
      renderTurnBlocks(log, Array.isArray(turn.blocks) ? turn.blocks : [], i === turns.length - 1, isRunning);
    }
    pending.forEach(item => renderPendingFollowup(log, item));
    restoreDetailUIState(log, uiSnapshot);
    normalizeDetailActivity(log, isRunning);
    restoreDetailScrollPosition(log, uiSnapshot);
  }

  function renderDetailMetadata(detail) {
    const host = byID('detail-meta-content');
    if (!host) return;
    if (!detail) {
      host.innerHTML = '<div class="meta-empty">No chat details yet.</div>';
      return;
    }

    const repoSlug = detailRepoSlug(detail);
    const branch = compact(detail.branch, '');
    const prURL = compact(detail.pr_url, '');
    const prHref = safeHttpURL(prURL);
    const sandbox = compact(detail.sandbox_id, '');
    const agent = compact(detail.agent_name, '') || (detail.agent_slug ? agentName(detail.agent_slug) : '');
    const model = compact(detail.model, '');
    const created = fullDate(detail.created_at || detail.updated_at);
    const creator = detail.creator_id ? userName(detail.creator_id) : '';
    const attachments = Array.isArray(detail.attachments) ? detail.attachments : [];
    const skills = Array.isArray(detail.sx_skills) ? detail.sx_skills : [];

    const repoHref = repoGitHubHref(repoSlug);
    const repoCell = repoHref
      ? '<a href="' + esc(repoHref) + '" target="_blank" rel="noopener">' + esc(repoSlug) + '</a>'
      : '<span>-</span>';
    const branchCell = repoSlug && branch
      ? '<a href="' + esc(repoGitHubHref(repoSlug)) + '/tree/' + encodeBranchPath(branch)
        + '" target="_blank" rel="noopener" class="meta-mono">' + esc(branch) + '</a>'
      : (branch ? '<span class="meta-mono">' + esc(branch) + '</span>' : '<span>-</span>');
    let prCell = '<span>-</span>';
    if (prHref) {
      const prNumber = extractPRNumber(prHref);
      prCell = '<a href="' + esc(prHref) + '" target="_blank" rel="noopener">'
        + esc(prNumber ? '#' + prNumber : prHref) + '</a>';
    }

    const rows = [
      { label: 'Agent', html: agent ? '<span>' + esc(agent) + '</span>' : '<span>-</span>', empty: !agent },
      { label: 'Model', html: model ? '<span>' + esc(modelLabel(model)) + '</span>' : '<span>-</span>', empty: !model },
      { label: 'Repo', html: repoCell, empty: !repoSlug },
      { label: 'Branch', html: branchCell, empty: !branch },
      { label: 'PR', html: prCell, empty: !prHref },
      { label: 'Attachments', html: buildDetailAttachmentsCell(attachments), empty: attachments.length === 0 },
      { label: 'Skills', html: buildDetailSkillsCell(skills), empty: skills.length === 0 },
      { label: 'Sandbox', html: sandbox ? '<span class="meta-mono">' + esc(sandbox) + '</span>' : '<span>-</span>', empty: !sandbox },
      { label: 'Created by', html: creator ? '<span>' + esc(creator) + '</span>' : '<span>-</span>', empty: !creator },
      { label: 'Created', html: created ? '<span>' + esc(created) + '</span>' : '<span>-</span>', empty: !created },
    ];

    const rowHTML = rows.map(row =>
      '<div class="meta-row' + (row.empty ? ' is-empty' : '') + '">'
      + '<span class="meta-label">' + esc(row.label) + '</span>'
      + '<span class="meta-value">' + row.html + '</span>'
      + '</div>'
    ).join('');

    host.innerHTML =
      '<div class="meta-section">'
      + '<div class="meta-title">Details</div>'
      + rowHTML
      + '</div>';

    setupDetailSkillsTrigger(skills);
  }

  function buildDetailAttachmentsCell(attachments) {
    if (!Array.isArray(attachments) || attachments.length === 0) return '<span>-</span>';
    return '<span class="meta-attachment-list">' + attachments.map(attachment => {
      const name = attachment.filename || 'attachment';
      const size = attachment.size_bytes
        ? ' <span class="meta-attachment-size">' + esc(attachmentSizeLabel(attachment.size_bytes)) + '</span>'
        : '';
      if (!attachment.download_url) {
        return '<span class="meta-attachment-link">' + esc(name) + size + '</span>';
      }
      const url = attachment.download_url;
      if (isImageAttachment(attachment.content_type)) {
        return '<button type="button" class="meta-attachment-link"'
          + ' data-image-modal-url="' + esc(url) + '"'
          + ' data-image-modal-name="' + esc(name) + '"'
          + ' aria-label="Open ' + esc(name) + '">'
          + esc(name) + size + '</button>';
      }
      return '<a class="meta-attachment-link" href="' + esc(url) + '" download="' + esc(name) + '">'
        + esc(name) + size + '</a>';
    }).join('') + '</span>';
  }

  function buildDetailSkillsCell(skills) {
    if (!Array.isArray(skills) || skills.length === 0) return '<span>-</span>';
    const preview = skills.slice(0, detailMaxSkillPreview);
    const previewHTML = preview.map(name =>
      '<span class="meta-chip">' + esc(name) + '</span>'
    ).join('');
    const remainder = skills.length - preview.length;
    const trailing = remainder > 0
      ? ' <button type="button" id="detail-skills-show-all" class="meta-show-all" aria-haspopup="dialog">'
        + 'Show all (' + esc(String(skills.length)) + ')</button>'
      : '';
    return '<span class="meta-chip-row">' + previewHTML + '</span>' + trailing;
  }

  function setupDetailSkillsTrigger(skills) {
    const btn = byID('detail-skills-show-all');
    if (!btn) return;
    btn.addEventListener('click', () => openDetailSkillsModal(skills));
  }

  function openDetailSkillsModal(skills) {
    byID('detail-skills-modal-overlay')?.remove();
    const overlay = document.createElement('div');
    overlay.id = 'detail-skills-modal-overlay';
    overlay.className = 'skills-modal-overlay';
    const titleID = 'detail-skills-modal-title';
    const items = skills.map(name => '<li class="skills-modal-item">' + esc(name) + '</li>').join('');
    overlay.innerHTML =
      '<div class="skills-modal" role="dialog" aria-modal="true" aria-labelledby="' + titleID + '" tabindex="-1">'
      + '<div class="skills-modal-head">'
      + '<h2 class="skills-modal-title" id="' + titleID + '">Installed skills (' + esc(String(skills.length)) + ')</h2>'
      + '<button type="button" class="skills-modal-close" aria-label="Close">&times;</button>'
      + '</div>'
      + '<ul class="skills-modal-list">' + items + '</ul>'
      + '</div>';

    const dialog = overlay.querySelector('.skills-modal');
    const previousFocus = document.activeElement;
    const close = () => {
      document.removeEventListener('keydown', onKey);
      overlay.remove();
      if (previousFocus && typeof previousFocus.focus === 'function' && document.contains(previousFocus)) {
        previousFocus.focus();
      }
    };
    const focusableSelector = [
      'a[href]', 'button:not([disabled])', 'input:not([disabled])',
      'select:not([disabled])', 'textarea:not([disabled])',
      '[tabindex]:not([tabindex="-1"])',
    ].join(',');
    const onKey = e => {
      if (e.key === 'Escape') {
        e.preventDefault();
        close();
        return;
      }
      if (e.key !== 'Tab') return;
      const items = Array.from(dialog.querySelectorAll(focusableSelector));
      if (!items.length) return;
      const first = items[0];
      const last = items[items.length - 1];
      const active = document.activeElement;
      if (e.shiftKey && (active === first || !dialog.contains(active))) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && (active === last || !dialog.contains(active))) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKey);
    overlay.addEventListener('click', e => {
      if (e.target === overlay) close();
    });
    overlay.querySelector('.skills-modal-close').addEventListener('click', close);
    const mount = byID('chat-detail-dialog')?.open ? byID('chat-detail-dialog') : document.body;
    mount.appendChild(overlay);
    overlay.querySelector('.skills-modal-close').focus();
  }

  // setupDetailMetaMenu wires the chat-header "..." menu once at init. The
  // menu lives in the static dialog header (not the re-rendered metadata
  // panel), so its actions read the active conversation from state and
  // reuse the same rename/delete dialogs as the run cards.
  function setupDetailMetaMenu() {
    const moreBtn = byID('detail-meta-more-btn');
    const dropdown = byID('detail-meta-dropdown');
    if (!moreBtn || !dropdown) return;
    moreBtn.addEventListener('click', e => {
      e.stopPropagation();
      const opening = dropdown.hidden;
      dropdown.hidden = !opening;
      moreBtn.setAttribute('aria-expanded', String(opening));
      if (opening) dropdown.querySelector('[role="menuitem"]')?.focus();
    });
    dropdown.addEventListener('keydown', e => {
      if (e.key === 'Escape') {
        e.preventDefault();
        closeDetailMetaDropdown();
        moreBtn.focus();
      }
    });
    byID('detail-download-btn')?.addEventListener('click', downloadActiveConversation);
    byID('detail-rename-btn')?.addEventListener('click', () => {
      const detail = state.activeDetail;
      closeDetailMetaDropdown();
      if (detail && detail.id) openRunRenameDialog(detail.id, detail.title || '');
    });
    byID('detail-delete-btn')?.addEventListener('click', () => {
      const detail = state.activeDetail;
      closeDetailMetaDropdown();
      if (detail && detail.id) openRunDeleteDialog(detail.id);
    });
  }

  function closeDetailMetaDropdown() {
    const dropdown = byID('detail-meta-dropdown');
    const moreBtn = byID('detail-meta-more-btn');
    if (dropdown) dropdown.hidden = true;
    if (moreBtn) moreBtn.setAttribute('aria-expanded', 'false');
  }

  async function downloadActiveConversation() {
    if (detailIsDownloading || !state.activeChatID) return;
    detailIsDownloading = true;
    closeDetailMetaDropdown();
    try {
      const detail = await fetchJSON('/api/v1/conversations/' + encodeURIComponent(state.activeChatID) + '?include=turns,attachments');
      const blob = new Blob([JSON.stringify(detail, null, 2) + '\n'], { type: 'application/json' });
      const filename = (detail.title || state.activeDetail?.title || 'conversation').replace(/[^a-z0-9_\-. ]/gi, '_') + '.json';
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
    } catch (err) {
      showToast('detail-download', 'Failed to download conversation.', 'error');
    } finally {
      detailIsDownloading = false;
    }
  }

  function appendUserMessage(parent, text) {
    const msg = document.createElement('div');
    msg.className = 'chat-message user';
    msg.textContent = text || '';
    parent.appendChild(msg);
  }
  function renderTurnBlocks(parent, blocks, isLastTurn, conversationRunning) {
    if (!blocks.length) return;
    if (typeof renderBlock !== 'function' || typeof openPhase !== 'function') {
      renderFallbackBlocks(parent, blocks);
      return;
    }
    const turn = document.createElement('div');
    turn.className = 'bot-turn';
    parent.appendChild(turn);
    let phase = null;
    let phaseLastEndedAt = '';
    let phaseHasActiveWork = false;
    for (const b of blocks) {
      if (b.kind === 'claude_text') {
        if (phase) closePhase(phase, phaseLastEndedAt || b.ended_at);
        phase = openPhase(turn, b.title, b.started_at);
        phase.proseRaw = b.body || '';
        phase.proseEl.innerHTML = renderMarkdown(phase.proseRaw);
        phaseLastEndedAt = b.ended_at || '';
        phaseHasActiveWork = blockIsStillActive(b);
      } else if (b.kind === 'tool_use') {
        if (!phase) phase = openPhase(turn, '', b.started_at);
        addToolToPhase(phase, {
          id: b.id,
          title: b.title,
          summary: b.summary,
          status: b.status,
          started_at: b.started_at,
          ended_at: b.ended_at,
        });
        if (b.ended_at) phaseLastEndedAt = b.ended_at;
        if (blockIsStillActive(b)) phaseHasActiveWork = true;
      } else {
        if (phase) {
          closePhase(phase, phaseLastEndedAt);
          phase = null;
          phaseLastEndedAt = '';
          phaseHasActiveWork = false;
        }
        renderBlock(turn, b, { open: isLastTurn && (b.kind === 'result' || b.kind === 'error') });
      }
    }
    if (phase) {
      if (isLastTurn && conversationRunning && phaseHasActiveWork) {
        phase.el.classList.add('blk-streaming');
        phase.el.open = true;
      } else if (isLastTurn && conversationRunning) {
        if (typeof markBlockAwaitingNext === 'function') markBlockAwaitingNext(phase.el);
        else phase.el.classList.add('blk-awaiting-next');
        phase.el.open = true;
      } else {
        closePhase(phase, phaseLastEndedAt);
      }
    }
  }
  function blockIsStillActive(block) {
    const status = compact(block && block.status, '').toLowerCase();
    return status === 'streaming' || status === 'running' || (!!block && !block.ended_at && status !== 'done' && status !== 'succeeded');
  }
  function normalizeDetailActivity(log, conversationRunning) {
    if (!log) return;
    const details = Array.from(log.querySelectorAll('details.blk'));
    if (!details.length) return;
    const active = details.filter(blockHasDetailActivity);
    if (!conversationRunning) {
      active.forEach(clearDetailActivity);
      return;
    }
    let latest = null;
    for (let i = active.length - 1; i >= 0; i--) {
      const candidate = active[i];
      if (!active.some(other => other !== candidate && candidate.contains(other))) {
        latest = candidate;
        break;
      }
    }
    if (!latest) {
      latest = details.slice().reverse().find(el =>
        !el.classList.contains('blk-kind-result') && !el.classList.contains('blk-kind-error')
      );
      if (latest) latest.classList.add('blk-awaiting-next');
    }
    details.forEach(el => {
      if (el !== latest) {
        clearDetailActivity(el);
        el.open = false;
      }
    });
    if (!latest) return;
    if (!blockHasDetailActivity(latest)) latest.classList.add('blk-awaiting-next');
    const latestIsTool = latest.classList.contains('blk-kind-tool_use');
    latest.open = !latestIsTool;
    const parentPhase = latest.closest('details.blk-kind-phase');
    if (parentPhase && parentPhase !== latest) {
      clearDetailActivity(parentPhase);
      parentPhase.open = true;
    }
  }
  function blockHasDetailActivity(el) {
    return !!el && (
      el.classList.contains('blk-streaming') ||
      el.classList.contains('blk-awaiting-next') ||
      el.classList.contains('blk-live-heartbeat')
    );
  }
  function clearDetailActivity(el) {
    if (!el) return;
    el.classList.remove('blk-streaming');
    el.classList.remove('blk-awaiting-next');
    el.classList.remove('blk-live-heartbeat');
  }
  function renderFallbackBlocks(parent, blocks) {
    blocks.forEach(block => {
      const box = document.createElement('div');
      box.className = 'chat-block' + (block.kind === 'error' || block.status === 'error' ? ' bot' : '');
      box.innerHTML = '<div class="chat-block-title"></div><div class="chat-block-body"></div>';
      box.querySelector('.chat-block-title').textContent = compact(block.title || block.kind, 'Update');
      box.querySelector('.chat-block-body').textContent = compact(block.body || block.summary, '');
      parent.appendChild(box);
    });
  }
  function visiblePendingFollowups(conversationID, turns) {
    const pending = pendingFollowupsFor(conversationID);
    if (!pending.length) return pending;
    const persisted = new Set((turns || []).map(turn => compact(turn.message, '')));
    const visible = pending.filter(item => !persisted.has(item.text));
    state.pendingFollowups[conversationID] = visible;
    return visible;
  }
  function pendingFollowupsFor(conversationID) {
    return state.pendingFollowups[conversationID] || [];
  }
  function addPendingFollowup(conversationID, text) {
    const pending = pendingFollowupsFor(conversationID).slice();
    pending.push({ text });
    state.pendingFollowups[conversationID] = pending;
  }
