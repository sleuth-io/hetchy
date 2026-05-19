const log = document.getElementById('log');
const inp = document.getElementById('inp');
const btn = document.getElementById('btn');
const toastStack = document.getElementById('toast-stack');
const toolsBtn = document.getElementById('tools-btn');
const toolsPopover = document.getElementById('tools-popover');
const agentSelectorBtn = document.getElementById('agent-selector-btn');
const agentSelectorValueEl = document.getElementById('agent-selector-value');
const agentPopover = document.getElementById('agent-popover');
const agentOptionsEl = document.getElementById('agent-options');
const repoSelectorBtn = document.getElementById('repo-selector-btn');
const repoSelectorValueEl = document.getElementById('repo-selector-value');
const repoPopover = document.getElementById('repo-popover');
const repoOptionsEl = document.getElementById('repo-options');
const repoSearchEl = document.getElementById('repo-search');
const modelBtn = document.getElementById('model-btn');
const modelLabelEl = document.getElementById('model-label');
const modelPopover = document.getElementById('model-popover');
const modelOptionsEl = document.getElementById('model-options');
const validateBox = document.getElementById('validate-checkbox');
const reviewBeforePushBox = document.getElementById('review-before-push-checkbox');
const actionPRChecksBox = document.getElementById('action-pr-checks-checkbox');
const attachmentInput = document.getElementById('attachment-input');
const attachmentListEl = document.getElementById('attachment-list');
const attachFilesBtn = document.getElementById('attach-files-btn');
const qualityOptionBoxes = [validateBox, reviewBeforePushBox, actionPRChecksBox].filter(Boolean);
const qualityOptionRows = document.querySelectorAll('.tools-checkbox-row');
const helpIcons = document.querySelectorAll('.tools-help');
const taskOptionKeys = {
  validate: 'validate',
  reviewBeforePush: 'review_code_before_push',
  actionPRChecks: 'action_pr_checks_for_done',
};

// Current user's WorkOS ID, injected by the server. Used to default the
// sidebar filter to "my chats".
const currentUserID = document.body.dataset.currentUserId || '';
let agentOptions = [];
let agentOptionsLoaded = false;
const agentStorageKey = 'hetchy.agent.' + currentUserID;

function readStoredAgentSlug() {
  try {
    const saved = localStorage.getItem(agentStorageKey);
    return saved === null ? '' : saved.trim();
  } catch (e) {
    return '';
  }
}

let selectedAgentSlug = readStoredAgentSlug();

// Repository picker state. Persisted per user so a fresh chat in the
// same browser starts on the last repo the user worked on, matching the
// Agent picker. The selection is a plain "owner/name" slug. Empty means
// no web selection yet; the composer blocks submit until one is chosen.
const repoStorageKey = 'hetchy.repo.' + currentUserID;
const orgDefaultRepoSlug = (document.body.dataset.defaultRepoSlug || '').trim();

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

function initialRepoSlug() {
  const stored = readStoredRepoSlug();
  return stored === null ? orgDefaultRepoSlug : stored;
}

let selectedRepoSlug = initialRepoSlug();
// repoOptions is the most-recent /api/v1/repositories response. The picker
// also injects the currently selected repo even when it falls outside
// that page, so a chat that was started against a rare repo still shows
// it in the dropdown after reload.
let repoOptions = [];
let repoOptionsLoaded = false;
let repoSearchQuery = '';
let repoLoadToken = 0;
const openAIEnabled = document.body.dataset.openaiEnabled === '1';
// The GPT block is appended only when the OpenAI Codex integration is
// configured on the org. The provider tag drives an optional section
// header in the dropdown so users can tell at a glance which family
// they're picking from — sticking the GPT options at the bottom keeps
// the Anthropic defaults in their familiar order for orgs that don't
// use OpenAI.
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
const modelStorageKey = 'hetchy.model';
let selectedModel = (function () {
  try {
    const saved = localStorage.getItem(modelStorageKey);
    if (modelOptions.some(model => model.value === saved)) return saved;
  } catch (e) {}
  return 'opus';
})();
let modelLocked = false;
let isRunning = false;
let isStopping = false;
let stopRequested = false;
let pendingAttachments = [];
const maxPromptAttachments = 5;
const maxPromptAttachmentBytes = 10 * 1024 * 1024;

function setRunState(running, stopping = false) {
  isRunning = running;
  isStopping = running && stopping;
  btn.classList.toggle('is-stop', isRunning);
  btn.disabled = isStopping;
  btn.setAttribute('aria-label', isRunning ? (isStopping ? 'Stopping' : 'Stop') : 'Send');
  btn.title = isRunning ? (isStopping ? 'Stopping…' : 'Stop') : 'Send';
  inp.setAttribute('aria-disabled', isRunning ? 'true' : 'false');
}

const activeToasts = new Map();
function showToast(key, message, kind = 'warn', timeoutMs = 0) {
  if (!toastStack) return;
  let entry = activeToasts.get(key);
  if (!entry) {
    const el = document.createElement('div');
    el.className = 'toast';
    el.setAttribute('role', 'status');
    toastStack.appendChild(el);
    entry = { el, timer: null };
    activeToasts.set(key, entry);
  }
  entry.el.className = 'toast toast-' + kind;
  entry.el.textContent = message;
  if (entry.timer) {
    clearTimeout(entry.timer);
    entry.timer = null;
  }
  if (timeoutMs > 0) {
    entry.timer = setTimeout(() => hideToast(key), timeoutMs);
  }
}

function hideToast(key) {
  const entry = activeToasts.get(key);
  if (!entry) return;
  if (entry.timer) clearTimeout(entry.timer);
  entry.el.remove();
  activeToasts.delete(key);
}

function handleComposerAction() {
  if (isRunning) {
    stopRun();
    return;
  }
  send();
}

// Read session ID from URL (?session=...) so the page can be reloaded or
// shared to reconnect to an existing sandbox. Generate a new one if absent.
// crypto.randomUUID() is gated on secure contexts; on plain HTTP at a
// non-localhost host (like dev.hetchy.ai) it isn't defined, so fall back
// to RFC4122 v4 from getRandomValues (which is unrestricted).
function newSessionId() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = [...b].map(x => x.toString(16).padStart(2, '0')).join('');
  return h.slice(0, 8) + '-' + h.slice(8, 12) + '-' + h.slice(12, 16) + '-' + h.slice(16, 20) + '-' + h.slice(20);
}
const params = new URLSearchParams(window.location.search);
const isFreshChat = !params.has('session');
const sessionId = params.get('session') || newSessionId();
let conversationHasServerState = !isFreshChat;
if (isFreshChat) {
  const url = new URL(window.location);
  url.searchParams.set('session', sessionId);
  history.replaceState(null, '', url);
}

// Auto-grow the textarea to fit its content. Collapsing the height to 0
// first sidesteps a Chromium quirk where an empty textarea's scrollHeight
// reflects the user-agent default rows rather than the actual content
// height. CSS min-height keeps the visible element at one line.
function autosizeInput() {
  inp.style.height = '0px';
  inp.style.height = inp.scrollHeight + 'px';
  // Once content exceeds the CSS max-height the element stops growing —
  // switch overflow back on so the user can scroll within the composer.
  inp.style.overflowY = inp.scrollHeight > inp.clientHeight ? 'auto' : 'hidden';
}
inp.addEventListener('input', autosizeInput);
// Bare Enter sends; Shift+Enter and Ctrl/Cmd+Enter insert a newline.
inp.addEventListener('keydown', e => {
  if (e.key !== 'Enter' || e.isComposing) return;
  if (e.shiftKey || e.ctrlKey || e.metaKey) {
    if (e.ctrlKey || e.metaKey) {
      // Browsers don't insert a newline for Ctrl/Cmd+Enter by default;
      // do it manually so the modifier behaves the same as Shift+Enter.
      e.preventDefault();
      const start = inp.selectionStart, end = inp.selectionEnd;
      inp.value = inp.value.slice(0, start) + '\n' + inp.value.slice(end);
      inp.selectionStart = inp.selectionEnd = start + 1;
      autosizeInput();
    }
    return;
  }
  e.preventDefault();
  send();
});

function addUserMsg(text, attachments = []) {
  // Replace the empty-state placeholder the first time we add a message
  // so the welcome card disappears as the conversation begins.
  const empty = document.getElementById('empty-state');
  if (empty) empty.remove();
  const d = document.createElement('div');
  d.className = 'msg user';
  if (Array.isArray(attachments) && attachments.length > 0) {
    const body = document.createElement('div');
    body.className = 'msg-text';
    body.textContent = text;
    d.appendChild(body);
    const list = document.createElement('div');
    list.className = 'msg-attachments';
    for (const attachment of attachments) {
      const name = attachment.filename || attachment.name || 'attachment';
      const item = document.createElement(attachment.download_url ? 'a' : 'span');
      item.className = 'msg-attachment';
      item.textContent = name;
      if (attachment.download_url) {
        item.href = attachment.download_url;
        item.download = name;
      }
      list.appendChild(item);
    }
    d.appendChild(list);
  } else {
    d.textContent = text;
  }
  log.appendChild(d);
  log.scrollTop = log.scrollHeight;
  return d;
}
