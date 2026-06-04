  var currentUserID = document.body.dataset.currentUserId || '';
  var defaultRepoSlug = (document.body.dataset.defaultRepoSlug || '').trim();
  var repoStorageKey = 'hetchy.repo.' + currentUserID;
  var openAIEnabled = document.body.dataset.openaiEnabled === '1';
  var appDataLimit = Math.max(1, parseInt(document.body.dataset.appDataLimit || '80', 10) || 80);
  var pollMs = 4000;
  var detailPollMs = 1800;
  var hiddenPollMs = 15000;
  var detailScrollBottomThreshold = 50;
  var recentRunWindowMs = 7 * 24 * 60 * 60 * 1000;
  var initialVisibleRuns = 8;
  var maxPromptAttachments = 5;
  var maxPromptAttachmentBytes = 10 * 1024 * 1024;
  var detailMaxSkillPreview = 4;
  var noAgentID = '__no_agent__';
  var unknownUserID = '__unknown_user__';
  var routeEmptyGroupSegment = '-';
  var validStatusFilters = new Set(['all', 'running', 'needs_input', 'failed', 'cancelled', 'ready_pr']);

  var modelOptions = [
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
  function readStoredModel() {
    try {
      const value = localStorage.getItem('hetchy.model');
      if (modelOptions.some(model => model.value === value)) return value;
    } catch (e) {}
    return 'opus';
  }

  var state = {
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
    selectedTaskRepo: initialTaskRepoSlug(),
    selectedTaskAgent: '',
    selectedTaskModel: readStoredModel(),
    runActionConversationID: '',
    runActionTitle: '',
    stopInFlightFor: '',
    detailStickToBottom: true,
    routeChatID: '',
    applyingRoute: false,
    suppressChatCloseSync: false,
  };
  var detailIsDownloading = false;
  var activeRunActionButton = null;

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
  function readStoredRepoSlug() {
    try {
      const saved = localStorage.getItem(repoStorageKey);
      if (saved === null) return null;
      const trimmed = saved.trim();
      if (!trimmed) return null;
      if (trimmed === '__hetchy_no_repository__') return null;
      return trimmed;
    } catch (e) {
      return null;
    }
  }
  function initialTaskRepoSlug() {
    const stored = readStoredRepoSlug();
    return stored === null ? defaultRepoSlug : stored;
  }
  function persistTaskRepoSlug() {
    try {
      if (state.selectedTaskRepo) localStorage.setItem(repoStorageKey, state.selectedTaskRepo);
      else localStorage.removeItem(repoStorageKey);
    } catch (e) {}
  }
  function chooseTaskRepo(slug) {
    state.selectedTaskRepo = compact(slug, '');
    persistTaskRepoSlug();
    renderRepoPicker();
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
  function skillDisplayName(name) {
    return compact(name, '').replace(/_skill$/i, '');
  }
  function skillKey(name) {
    return skillDisplayName(name).toLowerCase();
  }
  function agentSkillCount(agent) {
    if (!agent) return 0;
    const seen = new Set();
    for (const list of [agent.skills, agent.sx_skills]) {
      if (!Array.isArray(list)) continue;
      for (const name of list) {
        const key = skillKey(name);
        if (key) seen.add(key);
      }
    }
    return seen.size;
  }
  function agentTeamLabel(agent) {
    if (!agent || !Array.isArray(agent.sx_teams) || !agent.sx_teams.length) return '';
    const teams = agent.sx_teams.map(team => compact(team, '')).filter(Boolean);
    return teams.length ? 'Teams: ' + teams.join(', ') : '';
  }
  function agentHeaderSubtitle(id) {
    if (state.mode !== 'agent' || id === noAgentID) return groupDescription(id);
    const agent = agentForSlug(id);
    const parts = [groupDescription(id)];
    const teamLabel = agentTeamLabel(agent);
    if (teamLabel) parts.push(teamLabel);
    const count = agentSkillCount(agent);
    if (count) parts.push(count + ' ' + (count === 1 ? 'skill' : 'skills'));
    return parts.filter(Boolean).join(' · ');
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
  function runResultLabel(run) {
    if (!run) return statusLabel('done');
    const provided = compact(run.result_label, '');
    if (provided) return provided;
    if (run.status !== 'done') return statusLabel(run.status);
    if (compact(run.pr_url, '')) {
      if (run.pr_merged) return 'PR merged';
      return run.run_kind === 'followup' ? 'PR updated' : 'PR created';
    }
    return 'Answered';
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

  function encodeRouteSegment(value) {
    return encodeURIComponent(String(value || ''));
  }
  function decodeRouteSegment(value) {
    try {
      return decodeURIComponent(String(value || ''));
    } catch (e) {
      return String(value || '');
    }
  }
  function routeSegmentForSelectedGroup() {
    if (!state.selectedID) return '';
    if (state.mode === 'agent' && state.selectedID === noAgentID) return routeEmptyGroupSegment;
    if (state.mode === 'user' && state.selectedID === unknownUserID) return routeEmptyGroupSegment;
    return state.selectedID;
  }
  function selectedGroupFromRoute(mode, params, segments) {
    if (mode === 'user') {
      if (segments[0] === 'users' && segments.length > 1) {
        const segment = decodeRouteSegment(segments[1]);
        return segment === routeEmptyGroupSegment ? unknownUserID : segment;
      }
      if (params.has('user')) return compact(params.get('user'), unknownUserID);
      if (params.has('creator_id')) return compact(params.get('creator_id'), unknownUserID);
      return '';
    }
    if (segments[0] === 'agents' && segments.length > 1) {
      const segment = decodeRouteSegment(segments[1]);
      return segment === routeEmptyGroupSegment ? noAgentID : segment;
    }
    if (params.has('agent')) return compact(params.get('agent'), noAgentID);
    if (params.has('agent_slug')) return compact(params.get('agent_slug'), noAgentID);
    return '';
  }
  function parseRoute() {
    const params = new URLSearchParams(window.location.search);
    const segments = window.location.pathname.split('/').filter(Boolean);
    let mode = 'agent';
    const modeParam = compact(params.get('mode') || params.get('view'), '').toLowerCase();
    if (segments[0] === 'users' || modeParam === 'user' || modeParam === 'users') mode = 'user';
    if (segments[0] === 'agents' || modeParam === 'agent' || modeParam === 'agents') mode = 'agent';

    let routeChatID = '';
    if (segments[0] === 'chats' && segments.length > 1) {
      routeChatID = decodeRouteSegment(segments[1]);
    }
    routeChatID = compact(params.get('session') || params.get('chat') || routeChatID, '');

    const status = compact(params.get('status') || params.get('filter'), 'all');
    return {
      mode,
      selectedID: selectedGroupFromRoute(mode, params, segments),
      statusFilter: validStatusFilters.has(status) ? status : 'all',
      navQuery: compact(params.get('nav'), ''),
      workQuery: compact(params.get('q') || params.get('search'), ''),
      routeChatID,
    };
  }
  function setModeButtonState() {
    const agentBtn = byID('mode-agent');
    const userBtn = byID('mode-user');
    if (agentBtn) agentBtn.classList.toggle('is-active', state.mode === 'agent');
    if (userBtn) userBtn.classList.toggle('is-active', state.mode === 'user');
  }
  function syncRouteInputs() {
    setModeButtonState();
    const navSearch = byID('nav-search');
    const workSearch = byID('work-search');
    if (navSearch && navSearch.value !== state.navQuery) navSearch.value = state.navQuery;
    if (workSearch && workSearch.value !== state.workQuery) workSearch.value = state.workQuery;
  }
  function applyRouteState() {
    const route = parseRoute();
    state.applyingRoute = true;
    state.mode = route.mode;
    state.selectedID = route.selectedID;
    state.statusFilter = route.statusFilter;
    state.navQuery = route.navQuery;
    state.workQuery = route.workQuery;
    state.routeChatID = route.routeChatID;
    state.selectedRunLimit = initialVisibleRuns;
    state.workSearchKey = '';
    state.workSearch = null;
    syncRouteInputs();
    state.applyingRoute = false;
  }
  function currentRouteURL() {
    const params = new URLSearchParams();
    let path;
    if (state.activeChatID) {
      path = '/chats/' + encodeRouteSegment(state.activeChatID);
      params.set('view', state.mode === 'user' ? 'users' : 'agents');
      if (state.mode === 'user') {
        params.set('user', state.selectedID === unknownUserID ? '' : state.selectedID);
      } else {
        params.set('agent', state.selectedID === noAgentID ? '' : state.selectedID);
      }
    } else {
      const base = state.mode === 'user' ? '/users' : '/agents';
      const group = routeSegmentForSelectedGroup();
      path = group ? base + '/' + encodeRouteSegment(group) : base;
    }
    if (state.statusFilter && state.statusFilter !== 'all') params.set('status', state.statusFilter);
    if (state.workQuery.trim()) params.set('q', state.workQuery.trim());
    if (state.navQuery.trim()) params.set('nav', state.navQuery.trim());
    const query = params.toString();
    return path + (query ? '?' + query : '');
  }
  function syncRouteURL(opts) {
    if (state.applyingRoute || !window.history || !window.history.pushState) return;
    const next = currentRouteURL();
    const current = window.location.pathname + window.location.search;
    if (next === current) return;
    const method = opts && opts.replace ? 'replaceState' : 'pushState';
    window.history[method]({ app: true }, '', next);
  }
  function resetScopedWorkSearch() {
    state.selectedRunLimit = initialVisibleRuns;
    state.workSearchKey = '';
    state.workSearch = null;
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
