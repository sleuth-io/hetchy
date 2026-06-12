  async function fetchJSON(path) {
    const res = await fetch(path, { headers: { Accept: 'application/json' } });
    if (!res.ok) throw new Error(path + ' failed: ' + res.status);
    return await res.json();
  }

  async function loadSupportData() {
    const [agents, members] = await Promise.all([
      fetchJSON('/api/v1/agents').catch(() => []),
      fetchJSON('/api/v1/members').catch(() => []),
    ]);
    state.agents = Array.isArray(agents) ? agents : [];
    state.members = Array.isArray(members) ? members : [];
    renderAgentSelects();
    loadRepos('');
  }

  async function loadRepos(query) {
    const token = ++state.repoLoadToken;
    const params = new URLSearchParams();
    if (query) params.set('q', query);
    const path = '/api/v1/repositories' + (params.toString() ? '?' + params.toString() : '');
    try {
      const repos = await fetchJSON(path);
      if (token !== state.repoLoadToken) return;
      state.repos = Array.isArray(repos) ? repos : [];
      state.reposLoaded = true;
    } catch (e) {
      if (token !== state.repoLoadToken) return;
      state.repos = [];
      state.reposLoaded = true;
    }
    renderRepoPicker();
  }

  async function fetchAppData() {
    if (state.fetchInFlight) return;
    state.fetchInFlight = true;
    try {
      const params = new URLSearchParams();
      params.set('limit', String(appDataLimit));
      const data = await fetchJSON('/api/v1/app-data?' + params.toString());
      state.data = data || { runs: [], pull_requests: [], counts: {} };
      ensureSelection();
      renderAll();
      openRouteChat();
      syncRouteURL({ replace: true });
    } catch (e) {
      showToast('sync', 'Could not refresh work state.', 'warn');
    } finally {
      state.fetchInFlight = false;
      schedulePoll();
    }
  }

  function schedulePoll(delay) {
    clearTimeout(state.pollTimer);
    state.pollTimer = setTimeout(fetchAppData, delay || (document.hidden ? hiddenPollMs : pollMs));
  }
  function pollSoon() {
    schedulePoll(650);
  }
  function runForConversation(conversationID) {
    return allRuns().find(run => run.conversation_id === conversationID) || null;
  }
  function detailIsRunning(detail) {
    return compact(detail && detail.status, '').toLowerCase() === 'running';
  }
  function conversationIsRunning(conversationID) {
    if (!conversationID) return false;
    if (state.activeDetail && state.activeDetail.id === conversationID && detailIsRunning(state.activeDetail)) return true;
    const run = runForConversation(conversationID);
    return !!run && run.status === 'running';
  }
  function updateFollowupRunState(running) {
    const btn = byID('followup-send-btn');
    const input = byID('followup-input');
    if (!btn) return;
    const stopping = running && state.stopInFlightFor === state.activeChatID;
    btn.classList.toggle('is-stop', !!running);
    btn.disabled = !!stopping;
    btn.setAttribute('aria-label', running ? (stopping ? 'Stopping' : 'Stop') : 'Send follow-up');
    btn.title = running ? (stopping ? 'Stopping...' : 'Stop') : 'Send follow-up';
    if (input) input.setAttribute('aria-disabled', running ? 'true' : 'false');
  }
  function shouldPollActiveDetail() {
    const dialog = byID('chat-detail-dialog');
    if (!state.activeChatID || !dialog || !dialog.open) return false;
    if (state.activeDetail && state.activeDetail.id === state.activeChatID && detailIsRunning(state.activeDetail)) return true;
    const run = runForConversation(state.activeChatID);
    return !!run && run.status === 'running';
  }
  function stopDetailPoll() {
    clearTimeout(state.detailPollTimer);
    state.detailPollTimer = null;
  }
  function scheduleDetailPoll(delay) {
    clearTimeout(state.detailPollTimer);
    state.detailPollTimer = null;
    if (!shouldPollActiveDetail()) return;
    state.detailPollTimer = setTimeout(refreshActiveDetail, delay || (document.hidden ? hiddenPollMs : detailPollMs));
  }
  async function refreshActiveDetail() {
    if (!shouldPollActiveDetail()) {
      stopDetailPoll();
      return;
    }
    if (state.detailFetchInFlight) {
      scheduleDetailPoll(detailPollMs);
      return;
    }
    const conversationID = state.activeChatID;
    state.detailFetchInFlight = true;
    try {
      await loadChatDetail(conversationID, { quiet: true, preserveUI: true });
    } finally {
      state.detailFetchInFlight = false;
      if (state.activeChatID === conversationID) scheduleDetailPoll();
    }
  }

  function currentWorkSearchKey() {
    return [state.mode, state.selectedID, state.workQuery.trim()].join('\n');
  }
  function addSelectedGroupParams(params) {
    if (state.mode === 'user') {
      params.set('creator_id', state.selectedID === unknownUserID ? '' : state.selectedID);
    } else {
      params.set('agent_slug', state.selectedID === noAgentID ? '' : state.selectedID);
    }
  }
  async function fetchWorkSearch() {
    const query = state.workQuery.trim();
    if (!query || !state.selectedID || state.workSearchInFlight) {
      if (!query) {
        state.workSearchKey = '';
        state.workSearch = null;
        renderAll();
      }
      return;
    }
    const key = currentWorkSearchKey();
    state.workSearchInFlight = true;
    try {
      const params = new URLSearchParams();
      params.set('limit', String(appDataLimit));
      params.set('q', query);
      addSelectedGroupParams(params);
      const data = await fetchJSON('/api/v1/app-data?' + params.toString());
      if (currentWorkSearchKey() !== key) return;
      state.workSearchKey = key;
      state.workSearch = data || { runs: [], pull_requests: [] };
      renderAll();
    } catch (e) {
      if (currentWorkSearchKey() === key) {
        state.workSearchKey = key;
        state.workSearch = { runs: [], pull_requests: [] };
        renderAll();
      }
      showToast('work-search', 'Could not search chats for this view.', 'warn');
    } finally {
      state.workSearchInFlight = false;
      if (state.workQuery.trim() && state.workSearchKey !== currentWorkSearchKey()) {
        scheduleWorkSearch();
      }
    }
  }
  function scheduleWorkSearch() {
    clearTimeout(state.workSearchTimer);
    if (!state.workQuery.trim()) {
      state.workSearchKey = '';
      state.workSearch = null;
      renderAll();
      return;
    }
    state.workSearchTimer = setTimeout(fetchWorkSearch, 250);
  }

  function openRouteChat() {
    const conversationID = compact(state.routeChatID, '');
    if (!conversationID) {
      if (state.activeChatID) closeActiveChat({ syncURL: false });
      return;
    }
    if (state.activeChatID === conversationID && byID('chat-detail-dialog')?.open) return;
    openChat(conversationID, { syncURL: false });
  }

  function allRuns() {
    return Array.isArray(state.data.runs) ? state.data.runs : [];
  }
  function allPRs() {
    return Array.isArray(state.data.pull_requests) ? state.data.pull_requests : [];
  }
  function activeSearchData() {
    if (!state.workQuery) return null;
    const key = currentWorkSearchKey();
    if (state.workSearchKey !== key) return null;
    return state.workSearch || { runs: [], pull_requests: [] };
  }
  function activeRuns() {
    const search = activeSearchData();
    return search ? (Array.isArray(search.runs) ? search.runs : []) : allRuns();
  }
  function activePRs() {
    const search = activeSearchData();
    return search ? (Array.isArray(search.pull_requests) ? search.pull_requests : []) : allPRs();
  }
  function groupIDForRun(run) {
    if (state.mode === 'user') return compact(run.creator_id, unknownUserID);
    return compact(run.agent_slug, noAgentID);
  }
  function runMatchesSelectedGroup(run) {
    return !!state.selectedID && groupIDForRun(run) === state.selectedID;
  }
  function groupName(id) {
    if (state.mode === 'user') return userName(id);
    return id === noAgentID ? 'No agent' : agentName(id);
  }
  function compareGroupsByName(a, b) {
    const byName = String(a.name || '').localeCompare(String(b.name || ''), undefined, { sensitivity: 'base' });
    if (byName !== 0) return byName;
    return String(a.id || '').localeCompare(String(b.id || ''), undefined, { sensitivity: 'base' });
  }
  function makeEmptyGroup(id) {
    return { id, name: groupName(id), runs: [], active: 0, recent: 0, readyPRs: 0, section: '' };
  }
  function runIsRecent(run) {
    const ts = Date.parse(run.updated_at || run.created_at || '');
    if (!Number.isFinite(ts)) return false;
    return Date.now() - ts <= recentRunWindowMs;
  }
  function buildRunGroups() {
    const groups = new Map();
    const readyIDs = readyConversationIDsFrom(allPRs());
    for (const run of allRuns()) {
      const id = groupIDForRun(run);
      if (!groups.has(id)) {
        groups.set(id, makeEmptyGroup(id));
      }
      const group = groups.get(id);
      group.runs.push(run);
      if (run.status === 'running') group.active += 1;
      if (runIsRecent(run)) group.recent += 1;
      if (readyIDs.has(run.conversation_id)) group.readyPRs += 1;
    }
    return groups;
  }
  function buildAgentGroups() {
    const groups = buildRunGroups();
    for (const agent of state.agents) {
      const id = compact(agent.slug, '');
      if (!id) continue;
      if (!groups.has(id)) groups.set(id, makeEmptyGroup(id));
    }
    if (!groups.has(noAgentID)) groups.set(noAgentID, makeEmptyGroup(noAgentID));

    const custom = [];
    const builtIn = [];
    let noAgent = null;
    for (const group of groups.values()) {
      if (group.id === noAgentID) {
        group.section = 'none';
        noAgent = group;
        continue;
      }
      const agent = agentForSlug(group.id);
      if (agent && agent.built_in) {
        group.section = 'builtin';
        builtIn.push(group);
      } else {
        group.section = 'custom';
        custom.push(group);
      }
    }
    const out = custom.sort(compareGroupsByName);
    if (noAgent) out.push(noAgent);
    out.push.apply(out, builtIn.sort(compareGroupsByName));
    return out;
  }
  function buildGroups() {
    let out;
    if (state.mode === 'agent') {
      out = buildAgentGroups();
    } else {
      const groups = buildRunGroups();
      out = Array.from(groups.values()).sort(compareGroupsByName);
    }
    const needle = state.navQuery.trim().toLowerCase();
    if (needle) {
      out = out.filter(group => group.name.toLowerCase().includes(needle));
    }
    return out;
  }
  function buildAllGroups() {
    const prev = state.navQuery;
    state.navQuery = '';
    const groups = buildGroups();
    state.navQuery = prev;
    return groups;
  }
  function ensureSelection() {
    const groups = buildAllGroups();
    if (!groups.length) {
      state.selectedID = '';
      return;
    }
    if (!state.selectedID || !groups.some(group => group.id === state.selectedID)) {
      const firstWithWork = groups.find(group => group.runs.length > 0);
      state.selectedID = (firstWithWork || groups[0]).id;
      state.selectedRunLimit = initialVisibleRuns;
    }
  }

  function renderAll() {
    renderSummary();
    renderGroups();
    renderRuns();
    renderPRs();
  }

  function renderSummary() {
    const counts = selectedGroupCounts();
    const allCount = selectedGroupRuns().length;
    const filterCounts = {
      all: allCount,
      running: counts.running,
      needs_input: counts.needs_input,
      failed: counts.failed,
      cancelled: counts.cancelled,
      scheduled_job: counts.scheduled_jobs,
      ready_pr: counts.ready_prs,
    };
    if (state.statusFilter !== 'all' && !filterCounts[state.statusFilter]) {
      state.statusFilter = 'all';
    }
    byID('summary-all').textContent = String(allCount);
    byID('summary-running').textContent = String(counts.running);
    byID('summary-needs-input').textContent = String(counts.needs_input);
    byID('summary-failed').textContent = String(counts.failed);
    byID('summary-cancelled').textContent = String(counts.cancelled);
    byID('summary-scheduled-jobs').textContent = String(counts.scheduled_jobs);
    byID('summary-ready-prs').textContent = String(counts.ready_prs);
    document.querySelectorAll('[data-status-filter]').forEach(btn => {
      const filter = btn.dataset.statusFilter || 'all';
      btn.hidden = filter !== 'all' && !filterCounts[filter];
      btn.classList.toggle('is-active', filter === state.statusFilter);
    });
  }

  function selectedGroupRuns() {
    if (!state.selectedID) return [];
    return activeRuns().filter(runMatchesSelectedGroup);
  }
  function readyConversationIDs() {
    return readyConversationIDsFrom(activePRs());
  }
  function readyConversationIDsFrom(prs) {
    return new Set((prs || [])
      .filter(pr => pr.validation_passed && pr.review_passed)
      .map(pr => pr.conversation_id));
  }
  function selectedGroupCounts() {
    const readyIDs = readyConversationIDs();
    return selectedGroupRuns().reduce((counts, run) => {
      if (run.status === 'running') counts.running++;
      if (run.status === 'needs_input') counts.needs_input++;
      if (run.status === 'failed') counts.failed++;
      if (run.status === 'cancelled') counts.cancelled++;
      if (runIsScheduledJob(run)) counts.scheduled_jobs++;
      if (readyIDs.has(run.conversation_id)) counts.ready_prs++;
      return counts;
    }, { running: 0, needs_input: 0, failed: 0, cancelled: 0, scheduled_jobs: 0, ready_prs: 0 });
  }

  function runIsScheduledJob(run) {
    return compact(run && run.trigger_source, '') === 'job' || !!compact(run && run.job_id, '');
  }

  function updateNavSearchPlaceholder() {
    const input = byID('nav-search');
    if (!input) return;
    const label = state.mode === 'user' ? 'Search users' : 'Search agents';
    input.placeholder = label;
    input.setAttribute('aria-label', label);
  }

  function renderGroups() {
    updateNavSearchPlaceholder();
    const list = byID('group-list');
    const groups = buildGroups();
    if (!groups.length) {
      list.innerHTML = '<div class="empty">No work found.</div>';
      return;
    }
    const html = [];
    let previousSection = '';
    let builtInLabelRendered = false;
    for (const group of groups) {
      if (group.section && previousSection && group.section !== previousSection) {
        html.push('<div class="group-separator" role="separator"></div>');
      }
      if (group.section === 'builtin' && !builtInLabelRendered) {
        html.push('<div class="group-section-label">built in agents</div>');
        builtInLabelRendered = true;
      }
      if (group.section) previousSection = group.section;
      const activeClass = group.id === state.selectedID ? ' is-active' : '';
      const countClass = group.readyPRs > 0 ? ' has-ready-prs' : '';
      const sub = group.active > 0 ? group.active + ' running' : group.recent + ' recent';
      html.push('<button class="group-item' + activeClass + '" type="button" data-group-id="' + esc(group.id) + '">'
        + '<span><span class="group-name">' + esc(group.name) + '</span><span class="group-sub">' + esc(sub) + '</span></span>'
        + '<span class="group-count' + countClass + '">' + esc(group.readyPRs) + '</span>'
        + '</button>');
    }
    list.innerHTML = html.join('');
  }

  function selectedRuns() {
    let runs = selectedGroupRuns();
    if (state.statusFilter === 'ready_pr') {
      const readyIDs = readyConversationIDs();
      runs = runs.filter(run => readyIDs.has(run.conversation_id));
    } else if (state.statusFilter === 'scheduled_job') {
      runs = runs.filter(runIsScheduledJob);
    } else if (state.statusFilter !== 'all') {
      runs = runs.filter(run => run.status === state.statusFilter);
    }
    return runs.sort(compareRuns);
  }
  function sidebarPRs() {
    const runsByConversation = new Map(allRuns().map(run => [run.conversation_id, run]));
    return allPRs().filter(pr => {
      const run = runsByConversation.get(pr.conversation_id);
      if (run) return runMatchesSelectedGroup(run);
      if (state.mode === 'agent') {
        const slug = compact(pr.agent_slug, noAgentID);
        return slug === state.selectedID;
      }
      return false;
    });
  }
  function compareRuns(a, b) {
    const at = Date.parse(a.updated_at) || Date.parse(a.created_at) || 0;
    const bt = Date.parse(b.updated_at) || Date.parse(b.created_at) || 0;
    if (at !== bt) return bt - at;
    return compact(a.title, '').localeCompare(compact(b.title, ''));
  }

  function renderRuns() {
    const menuWasOpen = !!(state.runActionConversationID && !byID('run-action-menu')?.hidden);
    const menuConversationID = state.runActionConversationID;
    if (!menuWasOpen) closeRunActionMenu();
    else activeRunActionButton = null;
    const title = groupName(state.selectedID);
    byID('selection-title').textContent = title || 'Agent work';
    renderAgentHeaderSubtitle(state.selectedID);
    const showAgentMenu = state.mode === 'agent' && state.selectedID && state.selectedID !== noAgentID;
    const agentMenuBtn = byID('agent-menu-btn');
    if (agentMenuBtn) {
      agentMenuBtn.hidden = !showAgentMenu;
      if (!showAgentMenu) closePopover('agent-menu', 'agent-menu-btn');
    }
    byID('work-title').textContent = 'Recent work';
    const workSearch = byID('work-search');
    if (workSearch) {
      workSearch.placeholder = 'Search ' + (title || 'this view') + ' chats';
    }
    byID('work-subtitle').textContent = state.statusFilter === 'all'
      ? (state.workQuery ? 'Matching chats for this group.' : '')
      : state.statusFilter === 'scheduled_job'
        ? 'Scheduled job runs for this group.'
        : statusLabel(state.statusFilter) + ' work for this group.';

    const list = byID('run-list');
    const runs = selectedRuns();
    if (state.workQuery && !activeSearchData()) {
      list.innerHTML = '<div class="empty">Searching chats...</div>';
      byID('show-more-btn').hidden = true;
      return;
    }
    if (!runs.length) {
      list.innerHTML = '<div class="empty">No matching work.</div>';
      byID('show-more-btn').hidden = true;
      return;
    }
    const visible = runs.slice(0, state.selectedRunLimit);
    list.innerHTML = visible.map(renderRunCard).join('');
    if (menuWasOpen) {
      const nextButton = Array.from(list.querySelectorAll('[data-run-actions]'))
        .find(btn => btn.dataset.runActions === menuConversationID);
      if (nextButton) openRunActionMenu(nextButton, { force: true });
      else closeRunActionMenu();
    }
    const more = byID('show-more-btn');
    more.hidden = runs.length <= state.selectedRunLimit;
    more.textContent = 'Show more';
  }

  function renderRunMeta(run, agent, repo) {
    const parts = ['<span>' + esc(agent) + '</span>'];
    if (repo && repo !== 'No repository') {
      parts.push('<span>' + esc(repo) + '</span>');
    }
    const prHref = safeHttpURL(run.pr_url || '');
    if (prHref) {
      const prLabel = run.pr_number ? '#' + run.pr_number : (extractPRNumber(prHref) ? '#' + extractPRNumber(prHref) : 'PR');
      parts.push('<a class="run-meta-link" href="' + esc(prHref) + '" target="_blank" rel="noopener">' + esc(prLabel) + '</a>');
    }
    parts.push('<span>' + esc(relativeTime(run.updated_at)) + '</span>');
    return parts.join('<span class="run-meta-separator">-</span>');
  }

  function renderRunCard(run) {
    const agent = agentName(run.agent_slug);
    const repo = compact(run.repository, 'No repository');
    const meta = renderRunMeta(run, agent, repo);
    const isActive = run.status === 'running';
    const stopButton = isActive
      ? '<button class="run-stop-button" type="button" data-stop-run="' + esc(run.conversation_id) + '" title="Stop run" aria-label="Stop run">'
        + '<svg viewBox="0 0 24 24" aria-hidden="true" fill="currentColor"><rect x="7" y="7" width="10" height="10" rx="2"/></svg>'
        + '</button>'
      : '';
    const resultLabel = runResultLabel(run);
    const activeDetails = isActive
      ? '<div class="active-run-details">'
        + '<div class="live-panel">'
        + '<div class="activity-current">'
        + '<div class="activity-head"><span class="activity-eyebrow">Current step</span><span class="activity-kicker"><span class="activity-kicker-text">' + esc(currentMilestoneLabel(run)) + '</span></span></div>'
        + '<div class="activity-message">' + renderActivityMarkdown(run.activity, 'Waiting for latest activity.') + '</div>'
        + '</div>'
        + '</div>'
        + '</div>'
      : '';
    return '<article class="run-card' + (isActive ? ' has-live' : '') + '" data-run-card="' + esc(run.conversation_id) + '" role="button" tabindex="0" aria-label="Open chat details for ' + esc(run.title) + '">'
      + '<div class="run-top">'
      + '<div><div class="run-title">' + esc(run.title) + '</div><div class="run-meta">' + meta + '</div></div>'
      + '<div class="run-state-actions">'
      + '<button class="run-action-button" type="button" data-run-actions="' + esc(run.conversation_id) + '" data-run-title="' + esc(run.title) + '" data-run-status="' + esc(run.status) + '" aria-label="Chat actions" aria-haspopup="menu" aria-expanded="false">'
      + '<svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/><circle cx="5" cy="12" r="1"/></svg>'
      + '</button>'
      + stopButton
      + '<span class="pill ' + esc(run.status) + (resultLabel === 'PR created' ? ' pr-created' : resultLabel === 'PR updated' ? ' pr-updated' : resultLabel === 'PR closed' ? ' pr-closed' : '') + '">' + esc(resultLabel) + '</span>'
      + '</div>'
      + '</div>'
      + activeDetails
      + '</article>';
  }
  function currentMilestoneLabel(run) {
    const currentStep = compact(run.current_step, '');
    if (currentStep) return currentStep;
    const milestones = Array.isArray(run.milestones) ? run.milestones : [];
    const current = milestones.find(m => m && m.state === 'current' && m.label);
    if (current) return current.label;
    const done = milestones.filter(m => m && m.state === 'done' && m.label);
    if (done.length) return done[done.length - 1].label;
    return statusLabel(run.status || 'running');
  }
  function renderActivityMarkdown(text, fallback) {
    const raw = compact(text, '');
    let html = activityTextIsUseful(raw)
      ? (typeof renderMarkdown === 'function' ? renderMarkdown(raw) : esc(raw))
      : '';
    if (!renderedText(html)) {
      const next = compact(fallback, 'Waiting for latest activity.');
      html = typeof renderMarkdown === 'function' ? renderMarkdown(next) : esc(next);
    }
    return html;
  }
  function renderedText(html) {
    if (!html) return '';
    const scratch = document.createElement('div');
    scratch.innerHTML = html;
    return scratch.textContent.trim();
  }
  function activityTextIsUseful(text) {
    text = compact(text, '');
    if (!text || /^`{3,}\s*$/.test(text) || /^~{3,}\s*$/.test(text)) return false;
    return /[A-Za-z0-9]/.test(text);
  }

  function renderPRs() {
    const prs = sidebarPRs().slice().sort((a, b) => {
      const ar = a.validation_passed && a.review_passed ? 0 : 1;
      const br = b.validation_passed && b.review_passed ? 0 : 1;
      if (ar !== br) return ar - br;
      return (Date.parse(b.updated_at) || 0) - (Date.parse(a.updated_at) || 0);
    });
    byID('pr-count').textContent = String(prs.length);
    const list = byID('pr-list');
    if (!prs.length) {
      list.innerHTML = '<div class="empty">No pull requests</div>';
      return;
    }
    const scaryArt = '<pre class="scary-ascii-art">  ☠️  READY TO MERGE  ☠️\n ███████████████████\n██░░░░░░░░░░░░░░░██\n█░░ ◉    ◉ ░░░░░░░█\n█░░    ▼     ░░░░░░█\n█░░  ▔▔▔▔▔  ░░░░░░█\n██░░░░░░░░░░░░░░░██\n ███████████████████\n  ⚡  MERGING SOON  ⚡</pre>';
    list.innerHTML = scaryArt + prs.map(pr => {
      const number = pr.number ? '#' + pr.number : 'PR';
      const validation = pr.validation_required ? pr.validation_passed : true;
      const review = pr.review_required ? pr.review_passed : true;
      return '<article class="pr-card">'
        + '<div class="pr-main">'
        + '<a class="pr-title" href="' + esc(pr.url) + '" target="_blank" rel="noopener">' + esc(number + ' ' + pr.title) + '</a>'
        + '<div class="pr-meta">'
        + '<span>' + esc(agentName(pr.agent_slug)) + ' - <a class="pr-chat" href="/?session=' + encodeURIComponent(pr.conversation_id) + '" data-open-chat="' + esc(pr.conversation_id) + '">chat</a></span>'
        + '</div>'
        + '</div>'
        + '<div class="pr-checks" aria-label="Validation and review status">'
        + prCheckHTML('Validation', validation, validation ? 'Validation passed' : 'Validation pending')
        + prCheckHTML('Review', review, review ? 'Review checks passed' : 'Review checks pending')
        + '</div>'
        + '</article>';
    }).join('');
  }
  function prCheckHTML(label, done, title) {
    return '<span class="pr-check ' + (done ? 'is-checked' : '') + '" title="' + esc(title) + '">' + esc(label) + '</span>';
  }
