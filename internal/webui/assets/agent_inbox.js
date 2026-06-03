(function () {
  const currentUserID = document.body.dataset.currentUserId || '';
  const defaultRepoSlug = (document.body.dataset.defaultRepoSlug || '').trim();
  const openAIEnabled = document.body.dataset.openaiEnabled === '1';
  const inboxLimit = 80;
  const pollMs = 4000;
  const detailPollMs = 1800;
  const hiddenPollMs = 15000;
  const initialVisibleRuns = 8;
  const maxPromptAttachments = 5;
  const maxPromptAttachmentBytes = 10 * 1024 * 1024;
  const detailMaxSkillPreview = 4;
  const noAgentID = '__no_agent__';
  const unknownUserID = '__unknown_user__';

  const modelOptions = [
    { value: 'opus', label: 'Opus', description: 'Most capable', provider: 'anthropic' },
    { value: 'sonnet', label: 'Sonnet', description: 'Balanced everyday work', provider: 'anthropic' },
    { value: 'haiku', label: 'Haiku', description: 'Fastest', provider: 'anthropic' },
  ];
  if (openAIEnabled) {
    modelOptions.push(
      { value: 'gpt-frontier', label: 'GPT Frontier', description: 'Most capable GPT', provider: 'openai' },
      { value: 'gpt-balanced', label: 'GPT Balanced', description: 'Balanced everyday work', provider: 'openai' },
      { value: 'gpt-fastest', label: 'GPT Fastest', description: 'Fastest', provider: 'openai' },
    );
  }

  const state = {
    mode: 'agent',
    selectedID: '',
    statusFilter: 'all',
    navQuery: '',
    workQuery: '',
    workSearchKey: '',
    workSearch: null,
    workSearchInFlight: false,
    workSearchTimer: null,
    data: { runs: [], pull_requests: [], counts: {} },
    agents: [],
    members: [],
    repos: [],
    reposLoaded: false,
    repoSearch: '',
    repoLoadToken: 0,
    selectedRunLimit: initialVisibleRuns,
    fetchInFlight: false,
    pollTimer: null,
    detailPollTimer: null,
    detailFetchInFlight: false,
    activeChatID: '',
    activeDetail: null,
    pendingFollowups: {},
    taskAttachments: [],
    followupAttachments: [],
    selectedTaskRepo: defaultRepoSlug,
    selectedTaskAgent: '',
    selectedTaskModel: readStoredModel(),
  };
  let detailIsDownloading = false;

  function byID(id) { return document.getElementById(id); }
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, ch => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    })[ch]);
  }
  function compact(s, fallback) {
    s = String(s || '').trim();
    return s || fallback || '';
  }
  function humanizeSlug(slug) {
    slug = compact(slug, '');
    if (!slug) return 'No agent';
    return slug.split(/[-_]+/).filter(Boolean).map(part =>
      part.charAt(0).toUpperCase() + part.slice(1)
    ).join(' ');
  }
  function agentForSlug(slug) {
    slug = compact(slug, '');
    if (!slug) return null;
    return state.agents.find(agent => agent.slug === slug) || null;
  }
  function agentName(slug) {
    slug = compact(slug, '');
    if (!slug) return 'No agent';
    const found = agentForSlug(slug);
    return found ? found.display_name : humanizeSlug(slug);
  }
  function memberForUserID(userID) {
    userID = compact(userID, '');
    if (!userID || userID === unknownUserID) return null;
    return state.members.find(member => member.user_id === userID) || null;
  }
  function userName(userID) {
    userID = compact(userID, '');
    if (!userID || userID === unknownUserID) return 'Unknown user';
    const found = memberForUserID(userID);
    if (found) return found.display_name || found.email || userID;
    if (userID === currentUserID) return 'You';
    return userID;
  }
  function groupDescription(id) {
    if (state.mode === 'user') {
      const found = memberForUserID(id);
      if (found && found.email) return found.email;
      return 'Chats started by this user.';
    }
    if (id === noAgentID) return 'Chats without a selected agent.';
    const found = agentForSlug(id);
    if (found && compact(found.description, '')) return found.description;
    return 'Recent work for this agent.';
  }
  function modelLabel(value) {
    const found = modelOptions.find(model => model.value === value);
    return found ? found.label : 'Opus';
  }
  function statusLabel(status) {
    switch (status) {
    case 'running': return 'Running';
    case 'needs_input': return 'Needs input';
    case 'failed': return 'Failed';
    case 'cancelled': return 'Canceled';
    case 'done': return 'Done';
    default: return humanizeSlug(status || 'done');
    }
  }
  function relativeTime(value) {
    const d = new Date(value);
    if (isNaN(d.getTime())) return '';
    const sec = Math.max(0, Math.round((Date.now() - d.getTime()) / 1000));
    if (sec < 60) return 'just now';
    const min = Math.round(sec / 60);
    if (min < 60) return min + 'm ago';
    const hr = Math.round(min / 60);
    if (hr < 24) return hr + 'h ago';
    const day = Math.round(hr / 24);
    return day + 'd ago';
  }
  function fullDate(value) {
    if (!value) return '';
    const d = new Date(value);
    if (isNaN(d.getTime())) return '';
    return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  }
  function safeHttpURL(value) {
    if (!value) return '';
    try {
      const url = new URL(value);
      return url.protocol === 'https:' || url.protocol === 'http:' ? value : '';
    } catch (err) {
      return '';
    }
  }
  function extractPRNumber(value) {
    if (!value) return '';
    const match = String(value).match(/\/pull\/(\d+)(?:[/?#]|$)/);
    return match ? match[1] : '';
  }
  function encodeBranchPath(value) {
    return String(value || '').split('/').map(encodeURIComponent).join('/');
  }
  function repoGitHubHref(repoSlug) {
    repoSlug = compact(repoSlug, '');
    if (!repoSlug || !repoSlug.includes('/')) return '';
    return 'https://github.com/' + repoSlug.split('/').map(encodeURIComponent).join('/');
  }
  function detailRepoSlug(detail) {
    if (!detail) return '';
    const owner = compact(detail.github_owner, '');
    const repo = compact(detail.github_repo, '');
    return owner && repo ? owner + '/' + repo : '';
  }
  function attachmentSizeLabel(bytes) {
    const n = Number(bytes) || 0;
    if (n < 1024) return n + ' B';
    if (n < 1024 * 1024) return (n / 1024).toFixed(n < 10 * 1024 ? 1 : 0) + ' KB';
    return (n / (1024 * 1024)).toFixed(n < 10 * 1024 * 1024 ? 1 : 0) + ' MB';
  }
  function isImageAttachment(contentType) {
    const ct = String(contentType || '').toLowerCase().trim();
    return ct.startsWith('image/');
  }

  function showToast(key, message, kind, timeoutMs) {
    const stack = byID('toast-stack');
    if (!stack) return;
    let el = stack.querySelector('[data-toast-key="' + key + '"]');
    if (!el) {
      el = document.createElement('div');
      el.className = 'toast';
      el.dataset.toastKey = key;
      stack.appendChild(el);
    }
    el.className = 'toast ' + (kind || 'warn');
    el.textContent = message;
    if (timeoutMs !== 0) {
      clearTimeout(el._toastTimer);
      el._toastTimer = setTimeout(() => el.remove(), timeoutMs || 3500);
    }
  }

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

  async function fetchInbox() {
    if (state.fetchInFlight) return;
    state.fetchInFlight = true;
    try {
      const params = new URLSearchParams();
      params.set('limit', String(inboxLimit));
      const data = await fetchJSON('/api/v1/agent-inbox?' + params.toString());
      state.data = data || { runs: [], pull_requests: [], counts: {} };
      ensureSelection();
      renderAll();
    } catch (e) {
      showToast('sync', 'Could not refresh work state.', 'warn');
    } finally {
      state.fetchInFlight = false;
      schedulePoll();
    }
  }

  function schedulePoll(delay) {
    clearTimeout(state.pollTimer);
    state.pollTimer = setTimeout(fetchInbox, delay || (document.hidden ? hiddenPollMs : pollMs));
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
      params.set('limit', String(inboxLimit));
      params.set('q', query);
      addSelectedGroupParams(params);
      const data = await fetchJSON('/api/v1/agent-inbox?' + params.toString());
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
    return { id, name: groupName(id), runs: [], active: 0, section: '' };
  }
  function buildRunGroups() {
    const groups = new Map();
    for (const run of allRuns()) {
      const id = groupIDForRun(run);
      if (!groups.has(id)) {
        groups.set(id, makeEmptyGroup(id));
      }
      const group = groups.get(id);
      group.runs.push(run);
      if (run.status === 'running') group.active += 1;
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
    return new Set(activePRs()
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
      if (readyIDs.has(run.conversation_id)) counts.ready_prs++;
      return counts;
    }, { running: 0, needs_input: 0, failed: 0, cancelled: 0, ready_prs: 0 });
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
      const countClass = group.active > 0 ? ' has-active' : '';
      const sub = group.active > 0 ? group.active + ' running' : group.runs.length + ' recent';
      html.push('<button class="group-item' + activeClass + '" type="button" data-group-id="' + esc(group.id) + '">'
        + '<span><span class="group-name">' + esc(group.name) + '</span><span class="group-sub">' + esc(sub) + '</span></span>'
        + '<span class="group-count' + countClass + '">' + esc(group.runs.length) + '</span>'
        + '</button>');
    }
    list.innerHTML = html.join('');
  }

  function selectedRuns() {
    let runs = selectedGroupRuns();
    if (state.statusFilter === 'ready_pr') {
      const readyIDs = readyConversationIDs();
      runs = runs.filter(run => readyIDs.has(run.conversation_id));
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
    const rank = { running: 0, needs_input: 1, failed: 2, done: 3, cancelled: 4 };
    const ar = rank[a.status] == null ? 5 : rank[a.status];
    const br = rank[b.status] == null ? 5 : rank[b.status];
    if (ar !== br) return ar - br;
    return (Date.parse(b.updated_at) || 0) - (Date.parse(a.updated_at) || 0);
  }

  function renderRuns() {
    const title = groupName(state.selectedID);
    byID('selection-title').textContent = title || 'Agent work';
    byID('selection-subtitle').textContent = state.selectedID ? groupDescription(state.selectedID) : 'Recent work, active first.';
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
      ? (state.workQuery ? 'Matching chats for this group.' : 'Recent work, with active runs first.')
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
    const activeDetails = isActive
      ? '<div class="active-run-details">'
        + '<div class="live-panel">'
        + '<div class="activity-current">'
        + '<div class="activity-head"><span class="activity-kicker">' + esc(currentMilestoneLabel(run)) + '</span></div>'
        + '<div class="activity-message">' + renderActivityMarkdown(run.activity || 'Run is active.') + '</div>'
        + '</div>'
        + '</div>'
        + '</div>'
      : '';
    return '<article class="run-card' + (isActive ? ' has-live' : '') + '" data-run-card="' + esc(run.conversation_id) + '" role="button" tabindex="0" aria-label="Open chat details for ' + esc(run.title) + '">'
      + '<div class="run-top">'
      + '<div><div class="run-title">' + esc(run.title) + '</div><div class="run-meta">' + meta + '</div></div>'
      + '<span class="pill ' + esc(run.status) + '">' + esc(statusLabel(run.status)) + '</span>'
      + '</div>'
      + activeDetails
      + '</article>';
  }
  function currentMilestoneLabel(run) {
    const milestones = Array.isArray(run.milestones) ? run.milestones : [];
    const current = milestones.find(m => m && m.state === 'current' && m.label);
    if (current) return current.label;
    const done = milestones.filter(m => m && m.state === 'done' && m.label);
    if (done.length) return done[done.length - 1].label;
    return statusLabel(run.status || 'running');
  }
  function renderActivityMarkdown(text) {
    if (typeof renderMarkdown === 'function') return renderMarkdown(text || '');
    return esc(text || '');
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
      list.innerHTML = '<div class="empty">No pull requests yet.</div>';
      return;
    }
    list.innerHTML = prs.map(pr => {
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

  function renderAgentSelects() {
    renderAgentPicker();
    renderModelPicker();
    renderRepoPicker();
    updateTaskToolsButton();
  }
  function readStoredModel() {
    try {
      const value = localStorage.getItem('hetchy.model');
      if (modelOptions.some(model => model.value === value)) return value;
    } catch (e) {}
    return 'opus';
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
    const options = byID('task-agent-options');
    if (!options) return;
    const choices = [{ slug: '', display_name: 'No agent', description: 'Use Hetchy without a specialized persona.' }].concat(state.agents);
    options.innerHTML = choices.map(agent =>
      '<button class="agent-choice' + ((agent.slug || '') === state.selectedTaskAgent ? ' is-selected' : '') + '" type="button" data-agent-slug="' + esc(agent.slug || '') + '">'
      + '<span class="agent-choice-name">' + esc(agent.display_name || agent.slug || 'No agent') + '</span>'
      + (agent.description ? '<span class="agent-choice-desc">' + esc(agent.description) + '</span>' : '')
      + '</button>'
    ).join('');
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
    [
      ['task-tools-popover', 'task-tools-btn'],
      ['task-repo-popover', 'task-repo-btn'],
      ['task-agent-popover', 'task-agent-btn'],
      ['task-model-popover', 'task-model-btn'],
      ['agent-menu', 'agent-menu-btn'],
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
    }
    if (dialog.close) dialog.close();
    else dialog.removeAttribute('open');
  }

  function openNewTask() {
    byID('task-input').value = '';
    state.selectedTaskRepo = defaultRepoSlug;
    state.selectedTaskAgent = '';
    state.selectedTaskModel = readStoredModel();
    state.taskAttachments = [];
    renderAttachmentList('task');
    if (state.mode === 'agent' && state.selectedID !== noAgentID) {
      state.selectedTaskAgent = state.selectedID;
    }
    state.repoSearch = '';
    if (byID('task-repo-search')) byID('task-repo-search').value = '';
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
    backgroundTurn('/api/v1/conversations', payload);
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

  async function openChat(conversationID) {
    state.activeChatID = conversationID;
    closeChatMetaPanel();
    stopDetailPoll();
    openDialog('chat-detail-dialog');
    byID('chat-log').innerHTML = '<div class="empty">Loading...</div>';
    await loadChatDetail(conversationID);
    scheduleDetailPoll(250);
  }
  async function loadChatDetail(conversationID, opts) {
    try {
      const detail = await fetchJSON('/api/v1/conversations/' + encodeURIComponent(conversationID) + '?include=turns,attachments');
      if (state.activeChatID && state.activeChatID !== conversationID) return null;
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
  function captureDetailUIState(log) {
    const openStates = new Map();
    log.querySelectorAll('details.blk').forEach((el, index) => {
      openStates.set(detailOpenStateKey(el, index), !!el.open);
    });
    const scrollBottom = log.scrollHeight - log.scrollTop - log.clientHeight;
    return {
      openStates,
      scrollTop: log.scrollTop,
      wasPinned: scrollBottom < 24,
    };
  }
  function restoreDetailUIState(log, snapshot) {
    if (!snapshot) {
      log.scrollTop = log.scrollHeight;
      return;
    }
    log.querySelectorAll('details.blk').forEach((el, index) => {
      const key = detailOpenStateKey(el, index);
      if (snapshot.openStates.has(key)) el.open = snapshot.openStates.get(key);
    });
    log.scrollTop = snapshot.wasPinned ? log.scrollHeight : Math.min(snapshot.scrollTop, log.scrollHeight);
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
    if (!turns.length && !pending.length) {
      log.innerHTML = '<div class="empty">No turns yet.</div>';
      return;
    }
    log.innerHTML = '';
    const isRunning = detailIsRunning(detail) || runForConversation(detail.id)?.status === 'running';
    for (let i = 0; i < turns.length; i++) {
      const turn = turns[i] || {};
      appendUserMessage(log, turn.message || '');
      renderTurnBlocks(log, Array.isArray(turn.blocks) ? turn.blocks : [], i === turns.length - 1, isRunning);
    }
    pending.forEach(item => renderPendingFollowup(log, item));
    restoreDetailUIState(log, uiSnapshot);
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
      + '<div class="meta-header">'
      + '<div class="meta-title">Details</div>'
      + '<div class="meta-more-wrap">'
      + '<button id="detail-meta-more-btn" type="button" aria-label="More options" aria-haspopup="menu" aria-expanded="false">...</button>'
      + '<div id="detail-meta-dropdown" role="menu" aria-label="More options" hidden>'
      + '<button id="detail-download-btn" type="button" role="menuitem" class="meta-dropdown-item">Download</button>'
      + '</div>'
      + '</div>'
      + '</div>'
      + rowHTML
      + '</div>';

    setupDetailMetaMenu();
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

  function setupDetailMetaMenu() {
    const moreBtn = byID('detail-meta-more-btn');
    const dropdown = byID('detail-meta-dropdown');
    const downloadBtn = byID('detail-download-btn');
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
    if (downloadBtn) downloadBtn.addEventListener('click', downloadActiveConversation);
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
  function renderPendingFollowup(parent, item) {
    appendUserMessage(parent, item.text);
    const box = document.createElement('div');
    box.className = 'chat-block pending-followup';
    box.innerHTML = '<div class="chat-block-title">Follow-up queued</div>'
      + '<div class="chat-block-body">Hetchy will resume this conversation with the existing context.</div>';
    parent.appendChild(box);
  }

  async function sendFollowup() {
    const detail = state.activeDetail;
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
      state.selectedRunLimit = initialVisibleRuns;
      state.workSearchKey = '';
      state.workSearch = null;
      byID('mode-agent').classList.add('is-active');
      byID('mode-user').classList.remove('is-active');
      ensureSelection();
      renderAll();
      scheduleWorkSearch();
    });
    byID('mode-user').addEventListener('click', () => {
      state.mode = 'user';
      state.selectedID = '';
      state.selectedRunLimit = initialVisibleRuns;
      state.workSearchKey = '';
      state.workSearch = null;
      byID('mode-user').classList.add('is-active');
      byID('mode-agent').classList.remove('is-active');
      ensureSelection();
      renderAll();
      scheduleWorkSearch();
    });
    byID('group-list').addEventListener('click', e => {
      const btn = e.target.closest('[data-group-id]');
      if (!btn) return;
      state.selectedID = btn.dataset.groupId;
      state.selectedRunLimit = initialVisibleRuns;
      state.workSearchKey = '';
      state.workSearch = null;
      renderAll();
      scheduleWorkSearch();
      if (window.matchMedia('(max-width: 820px)').matches) closeResponsivePanels();
    });
    byID('nav-search').addEventListener('input', e => {
      state.navQuery = e.target.value.trim();
      renderGroups();
    });
    byID('work-search').addEventListener('input', e => {
      state.workQuery = e.target.value.trim();
      state.selectedRunLimit = initialVisibleRuns;
      state.workSearchKey = '';
      state.workSearch = null;
      renderAll();
      scheduleWorkSearch();
    });
    document.querySelectorAll('[data-status-filter]').forEach(btn => {
      btn.addEventListener('click', () => {
        const next = btn.dataset.statusFilter || 'all';
        state.statusFilter = state.statusFilter === next && next !== 'all' ? 'all' : next;
        state.selectedRunLimit = initialVisibleRuns;
        renderAll();
      });
    });
    byID('show-more-btn').addEventListener('click', () => {
      state.selectedRunLimit += initialVisibleRuns;
      renderRuns();
    });
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
    document.addEventListener('keydown', e => {
      if (e.key === 'Escape') {
        closeResponsivePanels();
        closeChatMetaPanel();
      }
    });
    byID('chat-meta-toggle').addEventListener('click', e => {
      e.stopPropagation();
      toggleChatMetaPanel();
    });
    byID('chat-meta-scrim').addEventListener('click', closeChatMetaPanel);
    byID('run-list').addEventListener('keydown', e => {
      if (e.key !== 'Enter' && e.key !== ' ') return;
      const card = e.target.closest('[data-run-card]');
      if (!card) return;
      e.preventDefault();
      openChat(card.dataset.runCard);
    });
    byID('new-task-btn').addEventListener('click', openNewTask);
    byID('new-task-form').addEventListener('submit', submitNewTask);
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
      if (e.target.matches('input[type="checkbox"]')) updateTaskToolsButton();
    });
    byID('task-repo-search').addEventListener('input', e => {
      state.repoSearch = e.target.value;
      renderRepoPicker();
      clearTimeout(byID('task-repo-search')._debounce);
      byID('task-repo-search')._debounce = setTimeout(() => loadRepos(state.repoSearch.trim()), 180);
    });
    byID('task-repo-search').addEventListener('keydown', e => {
      if (e.key === 'Enter') {
        e.preventDefault();
        const first = byID('task-repo-options').querySelector('.repo-choice[data-repo-slug]:not([data-repo-slug=""])');
        if (first) {
          state.selectedTaskRepo = first.dataset.repoSlug;
          closePopover('task-repo-popover', 'task-repo-btn');
          renderRepoPicker();
          byID('task-input').focus();
        }
      } else if (e.key === 'Escape') {
        e.preventDefault();
        closePopover('task-repo-popover', 'task-repo-btn');
        byID('task-repo-btn').focus();
      }
    });
    byID('followup-send-btn').addEventListener('click', sendFollowup);
    byID('followup-input').addEventListener('keydown', e => {
      if (e.key === 'Enter' && !e.shiftKey && !e.metaKey && !e.ctrlKey) {
        e.preventDefault();
        sendFollowup();
      }
    });
    document.addEventListener('click', e => {
      const repoChoice = e.target.closest('[data-repo-slug]');
      if (repoChoice && repoChoice.closest('#task-repo-options')) {
        state.selectedTaskRepo = repoChoice.dataset.repoSlug;
        closePopover('task-repo-popover', 'task-repo-btn');
        renderRepoPicker();
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
        closeDialog(closeBtn.dataset.closeDialog);
        if (closeBtn.dataset.closeDialog === 'chat-detail-dialog') {
          state.activeChatID = '';
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
      if (!e.target.closest('.composer-popover') && !e.target.closest('.floating-menu')) {
        closePopover('task-tools-popover', 'task-tools-btn');
        closePopover('task-repo-popover', 'task-repo-btn');
        closePopover('task-agent-popover', 'task-agent-btn');
        closePopover('task-model-popover', 'task-model-btn');
        closePopover('agent-menu', 'agent-menu-btn');
      }
    });
    byID('user-menu-btn').addEventListener('click', () => {
      const menu = byID('user-menu-dropdown');
      const open = menu.hidden;
      menu.hidden = !open;
      byID('user-menu-btn').setAttribute('aria-expanded', open ? 'true' : 'false');
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

  async function init() {
    bindEvents();
    syncResponsivePanels();
    renderAgentSelects();
    await loadSupportData();
    await fetchInbox();
  }

  init();
})();
