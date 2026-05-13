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
const modelBtn = document.getElementById('model-btn');
const modelLabelEl = document.getElementById('model-label');
const modelPopover = document.getElementById('model-popover');
const modelOptionsEl = document.getElementById('model-options');
const validateBox = document.getElementById('validate-checkbox');
const reviewBeforePushBox = document.getElementById('review-before-push-checkbox');
const actionPRChecksBox = document.getElementById('action-pr-checks-checkbox');
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
const modelOptions = [
  { value: 'opus', label: 'Opus', description: 'Most capable' },
  { value: 'sonnet', label: 'Sonnet', description: 'Balanced everyday work' },
  { value: 'haiku', label: 'Haiku', description: 'Fastest' },
];
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

function addUserMsg(text) {
  // Replace the empty-state placeholder the first time we add a message
  // so the welcome card disappears as the conversation begins.
  const empty = document.getElementById('empty-state');
  if (empty) empty.remove();
  const d = document.createElement('div');
  d.className = 'msg user';
  d.textContent = text;
  log.appendChild(d);
  log.scrollTop = log.scrollHeight;
  return d;
}

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

// formatBlockTime renders a block's started_at into a short HH:MM
// (24-hour, local) string for the summary chip. Returns '' for a
// missing/invalid input so callers can blindly assign the result —
// the .blk-time:empty CSS rule hides the element entirely when no
// time is known (e.g. an older persisted block predating the field).
function formatBlockTime(value) {
  if (!value) return '';
  const d = new Date(value);
  if (isNaN(d.getTime())) return '';
  const pad = n => String(n).padStart(2, '0');
  return pad(d.getHours()) + ':' + pad(d.getMinutes());
}

// formatBlockDuration turns a (started_at, ended_at) pair into a
// compact total-execution-time string ("420ms", "3.2s", "1m 4s",
// "12m 30s"). Returns '' on missing/invalid input or a non-positive
// delta — the .blk-duration:empty CSS rule then hides the chip.
function formatBlockDuration(startedAt, endedAt) {
  if (!startedAt || !endedAt) return '';
  const a = new Date(startedAt).getTime();
  const b = new Date(endedAt).getTime();
  if (isNaN(a) || isNaN(b)) return '';
  const ms = b - a;
  if (ms < 0) return '';
  if (ms < 1000) return ms + 'ms';
  const totalSec = Math.round(ms / 1000);
  if (totalSec < 60) {
    // sub-minute: show one decimal so 3.2s reads sharper than 3s.
    const s = (ms / 1000).toFixed(1).replace(/\.0$/, '');
    return s + 's';
  }
  const m = Math.floor(totalSec / 60);
  const s = totalSec % 60;
  return s === 0 ? m + 'm' : m + 'm ' + s + 's';
}

// Per-kind icon glyphs shown in the collapsed summary. Spinner takes
// over when a block is streaming, so this is just the resting icon.
const BLK_ICON = {
  setup:       '□', // ▢
  notify:      '●', // ●
  claude_text: '✎', // ✎
  tool_use:    '⚙', // ⚙
  result:      '✓', // ✓
  error:       '✗', // ✗
  phase:       '✎', // ✎ — phase boxes group prose + tools, share the prose icon
};

// renderMarkdown converts a markdown body into sanitized HTML. We
// trust marked's output shape but run it through DOMPurify because
// the body comes from Claude / sandbox stdout and could contain
// arbitrary text — some of which (e.g. tool result JSON) might look
// like an HTML tag to a careless parser.
function renderMarkdown(md) {
  if (!md) return '';
  const html = marked.parse(md, { breaks: true, gfm: true });
  const sanitized = DOMPurify.sanitize(html);
  // Post-process to add target="_blank" and rel="noopener" to all links
  const temp = document.createElement('div');
  temp.innerHTML = sanitized;
  temp.querySelectorAll('a').forEach(a => {
    if (!a.hasAttribute('target')) {
      a.setAttribute('target', '_blank');
    }
    if (!a.hasAttribute('rel')) {
      a.setAttribute('rel', 'noopener');
    } else if (!a.getAttribute('rel').includes('noopener')) {
      a.setAttribute('rel', a.getAttribute('rel') + ' noopener');
    }
  });
  return temp.innerHTML;
}

// startBotTurn ensures there's a container the streaming blocks are
// appended into. One container per user → bot exchange. The active
// block's icon spinner is the run's "still alive" indicator —
// there's no separate turn-level pulse because it would duplicate
// the spinner inside whichever block is currently streaming.
function startBotTurn() {
  const empty = document.getElementById('empty-state');
  if (empty) empty.remove();
  const d = document.createElement('div');
  d.className = 'bot-turn';
  log.appendChild(d);
  log.scrollTop = log.scrollHeight;
  return d;
}

// renderBlock builds a collapsible block element and appends it into
// the supplied turn container. Used for setup, notify, result, and
// error kinds — claude_text and tool_use go through phase boxes
// (openPhase / addToolToPhase) instead so the user sees the prose +
// tools as a single grouped unit.
//
// tool_use is still handled here for the standalone case (no phase
// has been opened yet), though in practice the phase model creates
// one on first contact. We keep the standalone branch so a future
// emitter that fires tool_use without a wrapping claude_text doesn't
// silently drop on the floor.
function renderBlock(turn, block, opts) {
  const isToolUse = block.kind === 'tool_use';
  const isOneShot = block.kind === 'notify'
                    || block.kind === 'result'
                    || block.kind === 'error';
  const open = !!(opts && opts.open) && !isToolUse;
  const el = document.createElement('details');
  el.className = 'blk blk-kind-' + block.kind
                 + (block.status === 'streaming' ? ' blk-streaming' : '')
                 + (block.status === 'error' ? ' blk-kind-error' : '')
                 + (isOneShot ? ' blk-oneshot' : '');
  if (open) el.open = true;
  el.dataset.blockId = block.id;
  el.dataset.startedAt = block.started_at || '';
  el.id = 'block-' + block.id;
  el.innerHTML = '<summary>'
    + '<span class="caret">▶</span>'
    + '<span class="blk-icon"><span>' + (BLK_ICON[block.kind] || '•') + '</span></span>'
    + '<span class="blk-title"></span>'
    + '<span class="blk-summary"></span>'
    + '<span class="blk-time"></span>'
    + '<span class="blk-duration"></span>'
    + '</summary><div class="blk-body"></div>';
  el.querySelector('.blk-title').textContent = block.title || '';
  el.querySelector('.blk-summary').textContent = block.summary || '';
  el.querySelector('.blk-time').textContent = formatBlockTime(block.started_at);
  el.querySelector('.blk-duration').textContent = formatBlockDuration(block.started_at, block.ended_at);
  if (!isToolUse) {
    el.querySelector('.blk-body').innerHTML = renderMarkdown(block.body || '');
  }
  if (isToolUse) {
    // Suppress the native <details> click-to-toggle so the body never
    // opens (it would reveal an empty pane).
    el.querySelector('summary').addEventListener('click', e => e.preventDefault());
  }
  turn.appendChild(el);
  return el;
}

// openPhase creates a new phase box ("group" of one Claude prose
// paragraph + the tool calls it triggers). The box stays expanded
// while it's the active phase; closePhase below collapses it and
// stamps the right-hand summary with the step count.
//
// The header title is set on creation from the first claude_text
// chunk's first line (or "Working" when a tool_use opens the phase
// without a leading prose chunk). The body holds two regions: a
// .phase-prose block that streams the rest of the paragraph, and a
// .phase-tools wrapper that subsequent tool_uses are appended into.
function openPhase(turn, headerTitle, startedAt) {
  const el = document.createElement('details');
  el.className = 'blk blk-kind-phase blk-streaming';
  el.open = true;
  el.innerHTML = '<summary>'
    + '<span class="caret">▶</span>'
    + '<span class="blk-icon"><span>' + BLK_ICON.phase + '</span></span>'
    + '<span class="blk-title"></span>'
    + '<span class="blk-summary"></span>'
    + '<span class="blk-time"></span>'
    + '<span class="blk-duration"></span>'
    + '</summary>'
    + '<div class="blk-body">'
    +   '<div class="phase-prose"></div>'
    +   '<div class="phase-tools"></div>'
    + '</div>';
  const phase = {
    el,
    titleEl: el.querySelector('.blk-title'),
    summaryEl: el.querySelector('.blk-summary'),
    durationEl: el.querySelector('.blk-duration'),
    proseEl: el.querySelector('.phase-prose'),
    toolsEl: el.querySelector('.phase-tools'),
    proseRaw: '',
    toolCount: 0,
    titleLocked: false,
    proseRenderQueued: false,
    startedAt: startedAt || '',
  };
  phase.titleEl.textContent = headerTitle || 'Working';
  el.querySelector('.blk-time').textContent = formatBlockTime(startedAt);
  if (headerTitle) phase.titleLocked = true;
  turn.appendChild(el);
  return phase;
}

// SERVER_CLOCK_NOTE: every duration chip is computed from a pair of
// server-stamped (started_at, ended_at) values that came over the
// SSE wire or out of the persisted blocks. We deliberately do NOT
// fall back to the browser clock for the missing endpoint — a laptop
// that just resumed from sleep, or a VM with bad NTP, would otherwise
// paint a duration tens of minutes off (or get clamped to '' by
// formatBlockDuration when the delta goes negative). Better to hide
// the chip than show a wrong number.

// closePhase collapses the phase and updates its right-hand summary
// with the step count so the user sees "5 steps" once it's done.
// Idempotent — safe to call on a phase that's already closed.
//
// endedAt (optional) drives the total-execution-time chip. Callers
// should pass a server-stamped value: replay uses the trailing tool's
// ended_at, live uses the started_at of whatever event is closing
// the phase (next block_start). When neither is available — e.g. the
// SSE stream dropped before a terminal frame — the chip is left empty
// rather than synthesising one from the browser clock (see
// SERVER_CLOCK_NOTE above).
function closePhase(phase, endedAt) {
  if (!phase || phase._closed) return;
  phase._closed = true;
  phase.el.classList.remove('blk-streaming');
  phase.el.classList.remove('blk-live-heartbeat');
  phase.el.open = false;
  if (phase.toolCount > 0) {
    phase.summaryEl.textContent = phase.toolCount === 1
      ? '1 step'
      : phase.toolCount + ' steps';
  }
  if (phase.durationEl && phase.startedAt && endedAt) {
    phase.durationEl.textContent = formatBlockDuration(phase.startedAt, endedAt);
  }
}

// appendPhaseProse buffers the delta into the phase's prose buffer
// and schedules a single rAF-coalesced re-render — same coalescing
// rationale as the original appendToBlock (avoid O(n²) full re-parse
// per token on long prose chunks).
//
// Once the prose contains its first newline the header title is
// frozen with `titleLocked = true`. Without that latch, every
// subsequent delta would re-run `indexOf('\n')` over the growing
// buffer for nothing — the title can't change after the first line
// is complete. Until the newline arrives, we keep updating the
// title from the running first-line preview so streaming feels live.
function appendPhaseProse(phase, delta) {
  if (!phase) return;
  phase.proseRaw += delta;
  if (!phase.titleLocked) {
    const nl = phase.proseRaw.indexOf('\n');
    const head = nl >= 0 ? phase.proseRaw.slice(0, nl) : phase.proseRaw;
    const firstLine = head.trim();
    if (firstLine) {
      phase.titleEl.textContent = firstLine.length > 100
        ? firstLine.slice(0, 100) + '…'
        : firstLine;
    }
    if (nl >= 0) phase.titleLocked = true;
  }
  if (phase.proseRenderQueued) return;
  phase.proseRenderQueued = true;
  requestAnimationFrame(() => {
    phase.proseRenderQueued = false;
    phase.proseEl.innerHTML = renderMarkdown(phase.proseRaw);
    log.scrollTop = log.scrollHeight;
  });
}

// finishPhaseProse flushes any pending render (so the last delta
// always lands) and commits the title. The phase itself stays open —
// the trailing tool_uses are still arriving and belong inside it.
function finishPhaseProse(phase) {
  if (!phase) return;
  phase.titleLocked = true;
  phase.proseEl.innerHTML = renderMarkdown(phase.proseRaw);
}

// addToolToPhase renders a tool_use one-liner inside the supplied
// phase. The phase header carries the start-time chip for the whole
// group, so individual step rows omit it — only the duration chip
// remains, which is per-step useful (e.g. "287 lines / 1.2s").
function addToolToPhase(phase, payload) {
  const el = document.createElement('details');
  el.className = 'blk blk-kind-tool_use'
                 + (payload.status === 'streaming' ? ' blk-streaming' : '')
                 + (payload.status === 'error' ? ' blk-kind-error' : '');
  el.dataset.blockId = payload.id;
  el.dataset.startedAt = payload.started_at || '';
  el.id = 'block-' + payload.id;
  el.innerHTML = '<summary>'
    + '<span class="caret">▶</span>'
    + '<span class="blk-icon"><span>' + BLK_ICON.tool_use + '</span></span>'
    + '<span class="blk-title"></span>'
    + '<span class="blk-summary"></span>'
    + '<span class="blk-duration"></span>'
    + '</summary><div class="blk-body"></div>';
  el.querySelector('.blk-title').textContent = payload.title || '';
  el.querySelector('.blk-summary').textContent = payload.summary || '';
  el.querySelector('.blk-duration').textContent = formatBlockDuration(payload.started_at, payload.ended_at);
  el.querySelector('summary').addEventListener('click', e => e.preventDefault());
  phase.toolsEl.appendChild(el);
  phase.toolCount++;
  return el;
}

// appendToBlock buffers the delta into the block's raw markdown and
// schedules a single rAF-coalesced re-render. A naive "re-render on
// every delta" would be O(n²): each chunk re-parses + re-sanitises the
// full body, which visibly stutters the browser on long Claude streams
// (thousands of small text deltas). Coalescing collapses bursts of
// deltas into one paint per frame.
function appendToBlock(el, delta) {
  if (!el) return;
  const body = el.querySelector('.blk-body');
  body.dataset.raw = (body.dataset.raw || '') + delta;
  scheduleBlockRender(el);
}

function scheduleBlockRender(el) {
  if (el._renderQueued) return;
  el._renderQueued = true;
  requestAnimationFrame(() => {
    el._renderQueued = false;
    // Collapsible <details> blocks hide the body when closed, so the
    // markdown render is wasted work — catch up on the next render
    // (or on finishBlock). Flat claude_text blocks are always visible,
    // so we always paint.
    if (el.tagName === 'DETAILS' && !el.open) return;
    const body = el.querySelector('.blk-body');
    body.innerHTML = renderMarkdown(body.dataset.raw || '');
    log.scrollTop = log.scrollHeight;
  });
}

function finishBlock(el, status, summary, endedAt) {
  if (!el) return;
  el.classList.remove('blk-streaming');
  el.classList.remove('blk-awaiting-next');
  el.classList.remove('blk-live-heartbeat');
  const summaryEl = el.querySelector('.blk-summary');
  if (summary && summaryEl) summaryEl.textContent = summary;
  if (status === 'error') {
    el.classList.add('blk-kind-error');
  }
  const durationEl = el.querySelector('.blk-duration');
  const startedAt = el.dataset.startedAt;
  if (durationEl && startedAt && endedAt) {
    // Server-stamped only — see SERVER_CLOCK_NOTE near closePhase.
    durationEl.textContent = formatBlockDuration(startedAt, endedAt);
  }
  // Force a final render so any pending deltas land before the block
  // is collapsed by the next block_start (a collapsed block skips its
  // queued render, so we'd otherwise miss the last few tokens).
  const body = el.querySelector('.blk-body');
  if (body) body.innerHTML = renderMarkdown(body.dataset.raw || '');
}

function markBlockAwaitingNext(el) {
  if (!el) return;
  el.classList.add('blk-awaiting-next');
}

function clearBlockActivity(el) {
  if (!el) return;
  el.classList.remove('blk-streaming');
  el.classList.remove('blk-awaiting-next');
  el.classList.remove('blk-live-heartbeat');
}

function blockHasActivity(el) {
  return !!el && (
    el.classList.contains('blk-streaming') ||
    el.classList.contains('blk-awaiting-next')
  );
}

function heartbeatSummary(title, body, elapsed) {
  if (elapsed) return 'Still running · ' + elapsed;
  const text = body || title || 'Still running';
  const m = text.match(/\b(?:for|running for)\s+(.+?)\s+[—–-]/i);
  if (m && m[1]) return 'Still running · ' + m[1].trim();
  return title || 'Still running';
}

function applyHeartbeatToActiveBlock(currentPhase, lastStandaloneEl, payload) {
  const summary = heartbeatSummary(payload.title, payload.delta, payload.elapsed);
  if (currentPhase && !currentPhase._closed) {
    currentPhase.summaryEl.textContent = summary;
    currentPhase.el.classList.add('blk-live-heartbeat');
    return;
  }
  if (blockHasActivity(lastStandaloneEl)) {
    const summaryEl = lastStandaloneEl.querySelector('.blk-summary');
    if (summaryEl) summaryEl.textContent = summary;
    lastStandaloneEl.classList.add('blk-live-heartbeat');
  }
}

function showEmptyState() {
  log.innerHTML = '';
  const d = document.createElement('div');
  d.id = 'empty-state';
  const ascii = `<div class="empty-brand"><pre class="empty-ascii">
  ██╗  ██╗███████╗████████╗ ██████╗██╗  ██╗██╗   ██╗
  ██║  ██║██╔════╝╚══██╔══╝██╔════╝██║  ██║╚██╗ ██╔╝
  ███████║█████╗     ██║   ██║     ███████║ ╚████╔╝
  ██╔══██║██╔══╝     ██║   ██║     ██╔══██║  ╚██╔╝
  ██║  ██║███████╗   ██║   ╚██████╗██║  ██║   ██║
  ╚═╝  ╚═╝╚══════╝   ╚═╝    ╚═════╝╚═╝  ╚═╝   ╚═╝
  </pre><div class="empty-tagline">&lt; your AI coding co-pilot /&gt;</div></div>`;
  d.innerHTML = ascii;
  log.appendChild(d);
}

// Sidebar state. `sidebarItems` is the cumulative list of rows the
// API has returned for the current (userFilter, searchQuery) view —
// loadSidebar() resets it on a fresh load and loadMoreSidebar()
// appends a page on each "Load more" click.
//
// `sidebarHasMore` flips true whenever a page comes back full
// (length === SIDEBAR_PAGE_SIZE), which is the server's way of
// saying "there might be another page" without sending a separate
// total-count query.
//
// `sidebarReqSeq` tags every in-flight request so a slow earlier
// fetch (e.g. for "f") can't clobber a faster later one (for "fo").
// Each call increments the counter and remembers its own id; the
// completion handler discards the response when the global counter
// has moved past it. This replaces a naive in-flight boolean that
// would silently drop the user's keystrokes during slow networks.
const SIDEBAR_PAGE_SIZE = 20;
// LOAD_MORE_LABEL is the canonical Load-more button text. Captured
// as a constant so renderSidebar() can reset the button to a known
// state (label + enabled) when a stale loadMoreSidebar() is
// superseded by a fresh loadSidebar() before its own cleanup ran.
const LOAD_MORE_LABEL = 'Load more';
let sidebarItems = [];
let sidebarHasMore = false;
let sidebarReqSeq = 0;
// Search query. Populated from #chat-search; debounced so we don't
// fire a request per keystroke. Empty string means "no search".
let searchQuery = '';
let searchDebounce = null;
// `userFilter` is the currently-selected creator_id for the sidebar filter.
// Empty string means "All". Persisted in localStorage so navigating to a
// chat (which reloads the page) doesn't snap the filter back to "Me".
// Defaults to the current user on first visit.
//
// Key is suffixed with the current user_id so a shared browser doesn't
// carry one user's saved filter into another's session — that user's
// id wouldn't match any option in the new account's dropdown, the
// dropdown would silently fall back to "All", and the sidebar would
// appear empty until the user manually re-picked.
const userFilterStorageKey = 'hetchy.userFilter.' + currentUserID;
let userFilter = (function () {
  try {
    const saved = localStorage.getItem(userFilterStorageKey);
    return saved === null ? currentUserID : saved;
  } catch (e) {
    return currentUserID;
  }
})();

function renderSidebar() {
  const list = document.getElementById('chat-list');
  const more = document.getElementById('chat-list-more');
  // Always reset the Load-more button's transient state (label +
  // disabled) before painting. Without this reset, a Load-more click
  // that gets superseded by a search keystroke leaves the button
  // stuck on "Loading…" / disabled when the search response paints
  // through here, because the superseded loadMoreSidebar() bails
  // before its own cleanup runs.
  const moreBtn = document.getElementById('chat-list-more-btn');
  moreBtn.disabled = false;
  moreBtn.textContent = LOAD_MORE_LABEL;
  const display = sidebarItems.slice();
  list.innerHTML = '';
  if (display.length === 0) {
    // The empty-state copy depends on whether the user is searching:
    // "no past chats" reads wrong when the user typed "asdf" and got
    // zero matches; tell them their search came up empty instead.
    list.innerHTML = searchQuery
      ? '<div class="empty">No chats match your search.</div>'
      : '<div class="empty">No past chats yet.</div>';
    more.hidden = true;
    return;
  }
  more.hidden = !sidebarHasMore;
  for (const c of display) {
    const wrap = document.createElement('div');
    const isActive = c.thread_id === sessionId;
    wrap.className = 'chat-item-wrap' + (isActive ? ' wrap-active' : '');

    const a = document.createElement('a');
    a.className = 'chat-item' + (isActive ? ' active' : '');
    a.href = '/?session=' + encodeURIComponent(c.thread_id);
    a.title = c.title;
    a.textContent = c.title;
    wrap.appendChild(a);

    const menuBtn = document.createElement('button');
    menuBtn.className = 'chat-menu-btn';
    menuBtn.setAttribute('aria-label', 'Chat options');
    menuBtn.setAttribute('aria-haspopup', 'menu');
    menuBtn.setAttribute('aria-expanded', 'false');
    menuBtn.textContent = '•••';
    menuBtn.addEventListener('click', e => {
      e.preventDefault();
      e.stopPropagation();
      toggleChatMenu(menuBtn, c.thread_id, c.title);
    });
    wrap.appendChild(menuBtn);

    list.appendChild(wrap);
  }
}

// --- Chat item context menu ---
// A single shared dropdown rendered into <body> as fixed-position so it
// isn't clipped by the sidebar's overflow-y: auto scroll container.
const chatMenuDrop = document.createElement('div');
chatMenuDrop.className = 'chat-menu-drop';
chatMenuDrop.setAttribute('role', 'menu');
chatMenuDrop.hidden = true;
document.body.appendChild(chatMenuDrop);

let activeMenuBtn = null;

function closeChatMenu() {
  if (activeMenuBtn) {
    activeMenuBtn.setAttribute('aria-expanded', 'false');
    activeMenuBtn = null;
  }
  chatMenuDrop.hidden = true;
}

function toggleChatMenu(btn, threadId, title) {
  if (activeMenuBtn === btn) { closeChatMenu(); return; }
  closeChatMenu();

  activeMenuBtn = btn;
  btn.setAttribute('aria-expanded', 'true');

  // Populate options
  chatMenuDrop.innerHTML = '';
  const renameItem = document.createElement('button');
  renameItem.className = 'chat-menu-drop-item';
  renameItem.setAttribute('role', 'menuitem');
  renameItem.textContent = 'Rename';
  renameItem.addEventListener('click', () => { closeChatMenu(); openRenameDialog(threadId, title); });
  const deleteItem = document.createElement('button');
  deleteItem.className = 'chat-menu-drop-item danger';
  deleteItem.setAttribute('role', 'menuitem');
  deleteItem.textContent = 'Delete';
  deleteItem.addEventListener('click', () => { closeChatMenu(); openDeleteDialog(threadId); });
  chatMenuDrop.appendChild(renameItem);
  chatMenuDrop.appendChild(deleteItem);

  // Position below the button, aligned to its right edge
  const rect = btn.getBoundingClientRect();
  chatMenuDrop.style.top = (rect.bottom + 4) + 'px';
  chatMenuDrop.style.left = '';
  chatMenuDrop.style.right = (window.innerWidth - rect.right) + 'px';
  chatMenuDrop.hidden = false;
}

document.addEventListener('click', e => {
  if (!chatMenuDrop.hidden && !chatMenuDrop.contains(e.target)) closeChatMenu();
});
document.addEventListener('keydown', e => {
  if (e.key === 'Escape' && !chatMenuDrop.hidden) closeChatMenu();
});
// The dropdown is body-positioned (fixed coords) so it doesn't move with
// the sidebar — close it on scroll instead of trying to track the button.
document.getElementById('chat-list').addEventListener('scroll', () => {
  if (!chatMenuDrop.hidden) closeChatMenu();
}, { passive: true });

// --- Rename dialog ---
const renameDialog = document.getElementById('rename-dialog');
const renameInput  = document.getElementById('rename-input');

document.getElementById('rename-cancel').addEventListener('click', () => renameDialog.close());
document.getElementById('rename-save').addEventListener('click', saveRename);
renameInput.addEventListener('keydown', e => { if (e.key === 'Enter') saveRename(); });

function openRenameDialog(threadId, currentTitle) {
  renameInput.value = currentTitle;
  renameInput.dataset.threadId = threadId;
  renameDialog.showModal();
  renameInput.select();
}

async function saveRename() {
  const threadId = renameInput.dataset.threadId;
  const newTitle = renameInput.value.trim();
  if (!newTitle) return;
  let res;
  try {
    res = await fetch('/api/conversations/' + encodeURIComponent(threadId), {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title: newTitle }),
    });
  } catch (_) {
    alert('Could not rename: network error. Try again.');
    return;
  }
  if (!res.ok) {
    alert('Could not rename: ' + res.status + ' ' + res.statusText);
    return;
  }
  renameDialog.close();
  loadSidebar();
}

// --- Delete confirmation dialog ---
const deleteDialog = document.getElementById('delete-dialog');

document.getElementById('delete-cancel').addEventListener('click', () => deleteDialog.close());
document.getElementById('delete-confirm').addEventListener('click', confirmDelete);

function openDeleteDialog(threadId) {
  deleteDialog.dataset.threadId = threadId;
  deleteDialog.showModal();
}

async function confirmDelete() {
  const threadId = deleteDialog.dataset.threadId;
  let res;
  try {
    res = await fetch('/api/conversations/' + encodeURIComponent(threadId), { method: 'DELETE' });
  } catch (_) {
    alert('Could not delete: network error. Try again.');
    return;
  }
  if (!res.ok) {
    alert('Could not delete: ' + res.status + ' ' + res.statusText);
    return;
  }
  deleteDialog.close();
  if (threadId === sessionId) {
    window.location.href = '/';
  } else {
    loadSidebar();
  }
}

// fetchSidebarPage hits /api/conversations with the current filter +
// search + offset. Returns the JSON array on success, null on
// transport failure (the caller renders a generic error in that case).
async function fetchSidebarPage(offset) {
  const params = new URLSearchParams();
  if (userFilter)  params.set('user', userFilter);
  if (searchQuery) params.set('q', searchQuery);
  params.set('limit', String(SIDEBAR_PAGE_SIZE));
  params.set('offset', String(offset));
  const res = await fetch('/api/conversations?' + params.toString(),
                          { headers: { 'Accept': 'application/json' } });
  if (!res.ok) return null;
  return (await res.json()) || [];
}

// loadSidebar refreshes the sidebar from page 0 — used on init, on
// filter/search changes, and after every send() so a brand-new chat's
// row (and any server-side title update) shows up without a manual
// reload. Order is by creation time, newest first, so existing rows
// keep their slot when their conversation gets a new turn and a
// brand-new chat lands at the top of page 0. Resets sidebarItems
// before re-rendering so stale rows from a wider previous query
// don't linger underneath the new (potentially shorter) result set.
//
// Stale-response handling: a fast typist can fire several searches
// in quick succession; we never block them, but we discard responses
// that were superseded by a newer request before they finished.
async function loadSidebar() {
  const myReq = ++sidebarReqSeq;
  const list = document.getElementById('chat-list');
  try {
    const page = await fetchSidebarPage(0);
    if (myReq !== sidebarReqSeq) return; // superseded
    if (page === null) {
      list.innerHTML = '<div class="empty">Could not load chats.</div>';
      document.getElementById('chat-list-more').hidden = true;
      return;
    }
    sidebarItems = page;
    sidebarHasMore = page.length === SIDEBAR_PAGE_SIZE;
    renderSidebar();
  } catch (e) {
    if (myReq !== sidebarReqSeq) return;
    list.innerHTML = '<div class="empty">Could not load chats.</div>';
    document.getElementById('chat-list-more').hidden = true;
  }
}

// loadMoreSidebar appends the next page worth of rows. Bound to the
// "Load more" button click. Uses the same sidebarReqSeq counter as
// loadSidebar so a fresh search firing mid-pagination cleanly
// discards any in-flight "next page" response.
async function loadMoreSidebar() {
  if (!sidebarHasMore) return;
  const myReq = ++sidebarReqSeq;
  const btn = document.getElementById('chat-list-more-btn');
  btn.disabled = true;
  btn.textContent = 'Loading…';
  try {
    const page = await fetchSidebarPage(sidebarItems.length);
    if (myReq !== sidebarReqSeq) return; // superseded by a fresh load
    if (page === null) {
      btn.textContent = 'Could not load — retry';
      return;
    }
    // The server can return fewer than PAGE_SIZE rows (last page),
    // exactly PAGE_SIZE (more available — keep the button), or zero
    // (race with a delete). We re-derive hasMore from the page size.
    sidebarItems = sidebarItems.concat(page);
    sidebarHasMore = page.length === SIDEBAR_PAGE_SIZE;
    renderSidebar();
    btn.textContent = LOAD_MORE_LABEL;
  } catch (e) {
    if (myReq !== sidebarReqSeq) return;
    btn.textContent = 'Could not load — retry';
  } finally {
    if (myReq === sidebarReqSeq) btn.disabled = false;
  }
}

// Member display-name cache. Populated by loadMembers() so renderMetadata()
// can show a creator's "First Last" instead of an opaque WorkOS user id.
const memberById = new Map();

// Tiny HTML escape — values that flow into innerHTML below come from
// server JSON (branch names, repo, etc.) so we can't trust them
// against angle brackets even though they're constrained at write
// time.
function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[c]);
}

// extractPRNumber pulls the trailing /pull/<n> segment out of a
// github.com PR URL. Returns null on anything that doesn't match so
// the caller can fall back to showing the full URL.
function extractPRNumber(url) {
  if (!url) return null;
  const m = url.match(/\/pull\/(\d+)(?:[/?#]|$)/);
  return m ? m[1] : null;
}

function fmtDate(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return '';
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}

// safeHttpURL returns the URL only if it parses and uses http(s).
// Defense-in-depth — `pr_url` is written by trusted server-side code
// today, but the field could one day be sourced from an external
// integration, and `esc()` only HTML-encodes characters; it would
// happily round-trip `javascript:alert(1)` into a clickable href.
function safeHttpURL(u) {
  if (!u) return '';
  try {
    const p = new URL(u);
    return (p.protocol === 'https:' || p.protocol === 'http:') ? u : '';
  } catch (e) {
    return '';
  }
}

// encodeBranchPath segment-encodes a git ref so a slashed branch name
// (e.g. `feat/login`) lands on the correct GitHub `/tree/<branch>`
// route. `encodeURIComponent(branch)` would turn the `/` into `%2F`
// and GitHub would resolve it to a "branch not found" page.
function encodeBranchPath(b) {
  return b.split('/').map(encodeURIComponent).join('/');
}

// setDocTitle keeps the browser tab title in sync with the current
// chat. With multiple chats open in tabs, "Hetchy" everywhere makes
// the tab strip useless — surfacing the chat title disambiguates them
// without forcing the user to hover for the URL preview.
function setDocTitle(title) {
  const t = (title || '').trim();
  document.title = t ? t + ' — Hetchy' : 'Hetchy';
}

// renderMetadata paints the right-hand sidebar from a conversationDetail
// JSON shape (the same payload loadHistory consumes). Re-entrant —
// safe to call any time the source data changes (e.g. when a live SSE
// run finishes and the PR URL has just been persisted).
function renderMetadata(detail) {
  const host = document.getElementById('meta-content');
  if (!detail) {
    applyConversationModel(null);
    applyConversationAgent(null);
    applyConversationTaskOptions(null);
    host.innerHTML = '<div class="meta-empty">No chat details yet.</div>';
    setDocTitle('');
    return;
  }
  applyConversationModel(detail);
  applyConversationAgent(detail);
  applyConversationTaskOptions(detail);
  setDocTitle(detail.title);
  const owner = detail.github_owner || '';
  const repo = detail.github_repo || '';
  const branch = detail.branch || '';
  const prURL = detail.pr_url || '';
  const sandbox = detail.sandbox_id || '';
  const agent = detail.agent_name || detail.agent_slug || '';
  const model = detail.model || '';
  const created = fmtDate(detail.created_at || detail.updated_at);

  const repoSlug = (owner && repo) ? owner + '/' + repo : '';
  const repoLink = repoSlug
    ? '<a href="https://github.com/' + esc(repoSlug) + '" target="_blank" rel="noopener">'
        + esc(repoSlug) + '</a>'
    : '<span>—</span>';

  const branchLink = (repoSlug && branch)
    ? '<a href="https://github.com/' + esc(repoSlug) + '/tree/' + encodeBranchPath(branch)
        + '" target="_blank" rel="noopener" class="meta-mono">' + esc(branch) + '</a>'
    : (branch ? '<span class="meta-mono">' + esc(branch) + '</span>' : '<span>—</span>');

  // The PR cell intentionally renders no state badge — the server only
  // tracks the URL, not whether the PR is open/merged/closed, so a
  // hard-coded "Open" pill quickly turns into a lie once the human
  // takes the PR over. Add a real `pr_state` field through
  // conversationDetail before bringing the badge back.
  const prHref = safeHttpURL(prURL);
  let prCell = '<span>—</span>';
  if (prHref) {
    const num = extractPRNumber(prHref);
    const label = num ? '#' + num : prHref;
    prCell = '<a href="' + esc(prHref) + '" target="_blank" rel="noopener">'
      + esc(label) + '</a>';
  }

  const creatorLabel = detail.creator_id
    ? (memberById.get(detail.creator_id) || detail.creator_id)
    : '';

  // Build rows. Each row is wrapped in is-empty class when the value
  // is missing so the dashes look intentionally placeholdered rather
  // than like a layout bug.
  const rows = [
    { label: 'Agent',   html: agent
        ? '<span>' + esc(agent) + '</span>'
        : '<span>—</span>', empty: !agent },
    { label: 'Model',   html: model
        ? '<span>' + esc(modelLabelFor(model)) + '</span>'
        : '<span>—</span>', empty: !model },
    { label: 'Repo',    html: repoLink,                 empty: !repoSlug },
    { label: 'Branch',  html: branchLink,               empty: !branch },
    { label: 'PR',      html: prCell,                   empty: !prURL },
    { label: 'Sandbox', html: sandbox
        ? '<span class="meta-mono">' + esc(sandbox) + '</span>'
        : '<span>—</span>', empty: !sandbox },
    { label: 'Created by', html: creatorLabel
        ? '<span>' + esc(creatorLabel) + '</span>'
        : '<span>—</span>', empty: !creatorLabel },
    { label: 'Created',  html: created
        ? '<span>' + esc(created) + '</span>'
        : '<span>—</span>', empty: !created },
  ];

  const rowHTML = rows.map(r =>
    '<div class="meta-row' + (r.empty ? ' is-empty' : '') + '">'
    +   '<span class="meta-label">' + esc(r.label) + '</span>'
    +   '<span class="meta-value">' + r.html + '</span>'
    + '</div>'
  ).join('');

  host.innerHTML =
    '<div class="meta-section">'
    +   '<div class="meta-header">'
    +     '<div class="meta-title">Details</div>'
    +     '<div class="meta-more-wrap">'
    +       '<button id="meta-more-btn" type="button" aria-label="More options"'
    +       ' aria-haspopup="menu" aria-expanded="false">&#x2026;</button>'
    +       '<div id="meta-dropdown" role="menu" aria-label="More options" hidden>'
    +         '<button id="download-btn" type="button" role="menuitem" class="meta-dropdown-item">'
    +           'Download</button>'
    +       '</div>'
    +     '</div>'
    +   '</div>'
    +   rowHTML
    + '</div>';

  setupMetaMenu();
}

function setupMetaMenu() {
  const moreBtn = document.getElementById('meta-more-btn');
  const dropdown = document.getElementById('meta-dropdown');
  const downloadBtn = document.getElementById('download-btn');
  if (!moreBtn || !dropdown) return;

  // DOM is rebuilt fresh by renderMetadata before each call, so no
  // prior listeners exist — just attach directly without clone/replace.
  moreBtn.addEventListener('click', e => {
    e.stopPropagation();
    const opening = dropdown.hidden;
    dropdown.hidden = !opening;
    moreBtn.setAttribute('aria-expanded', String(opening));
    if (opening) dropdown.querySelector('[role="menuitem"]')?.focus();
  });

  dropdown.addEventListener('keydown', e => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      dropdown.querySelector('[role="menuitem"]')?.focus();
    }
  });

  if (downloadBtn) downloadBtn.addEventListener('click', downloadConversation);
}

function closeMetaDropdown() {
  const dropdown = document.getElementById('meta-dropdown');
  const moreBtn = document.getElementById('meta-more-btn');
  if (dropdown) dropdown.hidden = true;
  if (moreBtn) moreBtn.setAttribute('aria-expanded', 'false');
}

let isDownloading = false;

async function downloadConversation() {
  if (isDownloading) return;
  isDownloading = true;
  closeMetaDropdown();

  try {
    const res = await fetch('/api/conversations/download/' + encodeURIComponent(sessionId));
    if (!res.ok) {
      alert('Failed to download conversation');
      return;
    }

    const blob = await res.blob();
    const filename = (lastDetail?.title || 'conversation').replace(/[^a-z0-9_\-. ]/gi, '_') + '.json';
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  } catch (e) {
    console.error('Download failed:', e);
    alert('Failed to download conversation');
  } finally {
    isDownloading = false;
  }
}

// Most recent conversationDetail we've seen for this session, kept so
// the post-stream refresh can re-render against the freshest payload
// without forcing a second fetch when nothing has changed.
let lastDetail = null;

function renderPendingMetadata(text) {
  conversationHasServerState = true;
  const existing = lastDetail || {};
  const now = new Date().toISOString();
  lastDetail = {
    ...existing,
    thread_id: sessionId,
    title: text || existing.title || 'New chat',
    agent_slug: selectedAgentSlug || '',
    agent_name: selectedAgentSlug ? selectedAgentName() : '',
    model: selectedModel,
    task_options: currentTaskOptions(),
    creator_id: existing.creator_id || currentUserID,
    created_at: existing.created_at || now,
    updated_at: now,
    history: text ? [text] : (existing.history || []),
    response_blocks: existing.response_blocks || [],
  };
  renderMetadata(lastDetail);
}

function conversationAgentIsMutable() {
  if (!conversationHasServerState) return true;
  if (!lastDetail) return false;
  return !(lastDetail.pr_url || '').trim();
}

function updateMutablePendingAgentMetadata() {
  if (!conversationAgentIsMutable() || !lastDetail) return;
  lastDetail.agent_slug = selectedAgentSlug || '';
  lastDetail.agent_name = selectedAgentSlug ? selectedAgentName() : '';
  renderMetadata(lastDetail);
}

async function refreshMetadata() {
  if (!conversationHasServerState) {
    renderMetadata(null);
    return;
  }
  try {
    const res = await fetch('/api/conversations/' + encodeURIComponent(sessionId),
                            { headers: { 'Accept': 'application/json' } });
    if (!res.ok) return;
    const detail = await res.json();
    lastDetail = detail;
    renderMetadata(detail);
  } catch (e) {
    // Leave the previous render in place on transient errors.
  }
}

async function loadHistory(opts) {
  opts = opts || {};
  const skipLastBotResponse = !!opts.skipLastBotResponse;
  // Fresh chat before the first send: don't probe the API, just show
  // the welcome state. Once the user submits the first turn, this page
  // still has isFreshChat=true, but the server conversation now exists.
  if (!conversationHasServerState) { showEmptyState(); renderMetadata(null); return; }
  try {
    const res = await fetch('/api/conversations/' + encodeURIComponent(sessionId),
                            { headers: { 'Accept': 'application/json' } });
    if (res.status === 404) {
      // Session id is in the URL but no conversation has been persisted yet
      // (e.g. the user opened a fresh URL with ?session= or the agent for a
      // brand-new chat hasn't completed its first turn). Treat as new chat.
      conversationHasServerState = false;
      showEmptyState();
      renderMetadata(null);
      return;
    }
    if (!res.ok) return;
    const detail = await res.json();
    lastDetail = detail;
    renderMetadata(detail);
    log.innerHTML = '';
    const history = detail.history || [];
    const turns = detail.response_blocks || [];
    for (let i = 0; i < history.length; i++) {
      addUserMsg(history[i]);
      const isLastTurn = i === history.length - 1;
      // skipLastBotResponse is set when we know a live SSE stream is
      // about to take over rendering for the active turn — leave the
      // user message in place but don't paint the (possibly stale)
      // persisted bot blocks; consumeSSEResponse will paint the
      // authoritative version next.
      if (isLastTurn && skipLastBotResponse) {
        continue;
      }
      const blocks = turns[i] || [];
      const turn = startBotTurn();
      // Reconstruct the live phase grouping from the persisted block
      // sequence. The streamed turn paints the same phase boxes as
      // it goes; replay does it in one shot from the recorded list.
      let phase = null;
      // Track the trailing tool's ended_at within a phase so the
      // collapsed phase shows a duration that spans prose start →
      // last tool finish, matching the live view's elapsed-time feel.
      let phaseLastEndedAt = '';
      for (const b of blocks) {
        if (b.kind === 'claude_text') {
          if (phase) closePhase(phase, phaseLastEndedAt || b.ended_at);
          phase = openPhase(turn, b.title, b.started_at);
          phase.proseRaw = b.body || '';
          phase.proseEl.innerHTML = renderMarkdown(phase.proseRaw);
          phaseLastEndedAt = b.ended_at || '';
        } else if (b.kind === 'tool_use') {
          if (!phase) phase = openPhase(turn, '', b.started_at);
          addToolToPhase(phase, {
            id: b.id, title: b.title, summary: b.summary,
            status: b.status, started_at: b.started_at, ended_at: b.ended_at,
          });
          if (b.ended_at) phaseLastEndedAt = b.ended_at;
        } else {
          if (phase) { closePhase(phase, phaseLastEndedAt); phase = null; phaseLastEndedAt = ''; }
          const open = isLastTurn && (b.kind === 'result' || b.kind === 'error');
          renderBlock(turn, b, { open });
        }
      }
      if (phase) closePhase(phase, phaseLastEndedAt);
    }
    // Honour #block-<id> deep links by expanding + scrolling to the
    // referenced block.
    if (window.location.hash && window.location.hash.startsWith('#block-')) {
      const target = document.getElementById(window.location.hash.slice(1));
      if (target) {
        target.open = true;
        target.scrollIntoView({ behavior: 'smooth', block: 'center' });
      }
    }
  } catch (e) {
    // Leave the log empty on error; the user can still type a message.
  }
}

// Favicon swap — keeps a "ready" badge on the tab while the user is
// elsewhere. The H mark lives only in the static <link> in <head>; we
// read its href as the idle variant and splice a green dot into the
// URL-encoded data URL (right before the encoded "</svg>") to build
// the badged variant. One copy of the markup, no drift risk.
// Case-insensitive match on the closing tag because the URL spec
// doesn't pin percent-encoding casing — a future browser or tooling
// pass that lowercases hex digits would otherwise silently no-op the
// replace and leave FAVICON_DONE === FAVICON_IDLE. The post-replace
// warn surfaces any other breakage (e.g. the source SVG losing its
// closing tag) instead of letting the badge just stop appearing.
// Defined above consumeSSEResponse so the call site's dependency on
// FAVICON_DONE is lexical, not just hoisted-and-lucky.
const FAVICON_IDLE = (document.getElementById('favicon') || {}).href || '';
const FAVICON_DONE = FAVICON_IDLE.replace(
  /%3C%2Fsvg%3E/i,
  // encodeURIComponent leaves single quotes untouched (they're "mark"
  // characters per RFC 2396), but the static <link> href has its
  // quotes URL-encoded as %27 — post-process so the spliced fragment
  // matches the surrounding encoding instead of producing a hybrid.
  (m) => encodeURIComponent("<circle cx='24' cy='8' r='7' fill='#22c55e' stroke='#0d1117' stroke-width='2'/>").replace(/'/g, '%27') + m
);
if (FAVICON_IDLE && FAVICON_DONE === FAVICON_IDLE) {
  console.warn('Favicon badge build failed: closing </svg> not found in favicon href');
}
// Tracks whether the favicon currently shows the "done" badge, so we
// only rewrite link.href on visibilitychange when something actually
// needs to change — avoids a re-decode on every tab focus.
let faviconBadged = false;
function setFaviconHref(href) {
  const link = document.getElementById('favicon');
  if (link) link.href = href;
}
function signalTurnDone() {
  if (document.hidden) {
    setFaviconHref(FAVICON_DONE);
    faviconBadged = true;
  }
}
// Clear the badge as soon as the user looks at the tab again. Using
// visibilitychange (not focus) so switching browser windows without
// changing tabs doesn't keep the badge stuck on.
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && faviconBadged) {
    setFaviconHref(FAVICON_IDLE);
    faviconBadged = false;
  }
});

function newStreamRenderState() {
  return {
    turn: startBotTurn(),
    blockRefs: new Map(),
    currentPhase: null,
    phaseLastEndedAt: '',
    lastStandaloneEl: null,
    sawTurnEnd: false,
    lastSeq: 0,
  };
}

function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

async function openChatStream(afterSeq = 0) {
  const params = new URLSearchParams({ session: sessionId });
  if (afterSeq > 0) params.set('after_seq', String(afterSeq));
  return fetch('/chat/stream?' + params.toString(), {
    headers: { 'Accept': 'text/event-stream' },
  });
}

async function streamTurnWithReconnect(initialRes, options = {}) {
  const state = newStreamRenderState();
  let res = initialRes;
  let attempts = 0;
  let onFirstEvent = options.onFirstEvent || null;
  let firstStreamEventSeen = false;
  let sawDisconnect = false;
  let waitingBlock = null;

  const clearWaitingBlock = () => {
    if (!waitingBlock) return;
    waitingBlock.remove();
    waitingBlock = null;
  };
  if (options.waitingTitle || options.waitingBody) {
    waitingBlock = renderBlock(state.turn, {
      id: 'reattach-waiting',
      kind: 'notify',
      title: options.waitingTitle || 'Reconnecting',
      body: options.waitingBody || '',
      status: 'streaming',
    }, { open: true });
  }

  const finishStopped = async () => {
    hideToast('stream-disconnected');
    clearWaitingBlock();
    await loadHistory();
    return { terminal: true, state };
  };

  while (true) {
    try {
      if (stopRequested) return await finishStopped();
      if (!res) res = await openChatStream(state.lastSeq);
      if (!res.ok) {
        if (res.body) {
          try { await res.body.cancel(); } catch (e) {}
        }
        if (res.status === 404) {
          hideToast('stream-disconnected');
          clearWaitingBlock();
          await loadHistory();
          return { terminal: true, state };
        }
        if (res.status === 409) {
          const err = new Error('active run is reconnecting');
          err.reattachPending = true;
          throw err;
        }
        throw new Error('chat stream failed: ' + res.status);
      }

      if (sawDisconnect) {
        hideToast('stream-disconnected');
        showToast('stream-reconnected', 'Reconnected. Streaming has resumed.', 'success', 2500);
        sawDisconnect = false;
      }

      const result = await consumeSSEResponse(res, onFirstEvent ? () => {
        clearWaitingBlock();
        if (firstStreamEventSeen) return;
        firstStreamEventSeen = true;
        onFirstEvent();
      } : clearWaitingBlock, state);
      if (result.terminal) {
        hideToast('stream-disconnected');
        clearWaitingBlock();
        return result;
      }
      if (stopRequested) return await finishStopped();
      throw new Error('chat stream closed before the turn finished');
    } catch (e) {
      if (stopRequested) return await finishStopped();
      sawDisconnect = true;
      const msg = e && e.reattachPending
        ? 'Run is still active. Reconnecting…'
        : 'Connection lost. Retrying…';
      showToast('stream-disconnected', msg, 'warn');
      attempts += 1;
      const delay = Math.min(10000, 1000 * Math.pow(2, Math.min(attempts - 1, 4)));
      await sleep(delay);
      res = null;
    }
  }
}

// consumeSSEResponse reads a server-sent-event stream from `res` and
// renders blocks into a bot turn render state. Used by both the original
// POST-/chat send() flow and the GET-/chat/stream reload reattach
// flow — the event shape is identical, only the source URL differs.
//
// onFirstEvent (optional) fires exactly once, the first time we
// successfully parse a payload. send() uses it to refresh the sidebar
// the moment the server confirms work has begun — by then the
// conversation row has been Upserted and the LHN can render the new
// chat without an optimistic placeholder.
async function consumeSSEResponse(res, onFirstEvent, state = null) {
  if (!state) state = newStreamRenderState();
  let firstEventFired = false;
  const turn = state.turn;
  // blockRefs is the per-id map of "what to do on append/done". Each
  // ref is a tagged shape:
  //   { kind: 'phase-prose', phase }      — claude_text inside a phase
  //   { kind: 'tool',        el, phase }  — tool_use inside a phase
  //   { kind: 'standalone',  el }         — setup/notify/result/error
  // Phase-prose appends update the phase body; tool appends are
  // dropped (the live emitter never fires them); standalone appends
  // go through the original appendToBlock path.
  const blockRefs = state.blockRefs;
  // currentPhase is the open phase box accepting new tool_uses. Set
  // when claude_text or the first tool_use opens one; cleared when a
  // standalone block (setup/notify/result/error) starts and closes
  // it.
  let currentPhase = state.currentPhase;
  // phaseLastEndedAt mirrors the replay-path field: the server-
  // stamped ended_at of the trailing tool/prose block in the
  // currently-open phase, used as the phase's closing instant so the
  // collapsed phase shows a duration even though the wire has no
  // explicit "phase_done" frame. Reset whenever currentPhase resets.
  // Server-stamped only — never use the browser clock here (see
  // SERVER_CLOCK_NOTE in this file).
  let phaseLastEndedAt = state.phaseLastEndedAt;
  // lastStandaloneEl is the most recently rendered standalone block
  // (setup, notify, result, error). When the *next* block of any
  // kind starts, we collapse this one so the user always sees the
  // freshest activity expanded — e.g. the long Sandbox setup body
  // tucks itself away when Claude starts streaming its first phase.
  let lastStandaloneEl = state.lastStandaloneEl;
  // Set to true once a terminal standalone block (result/error) is
  // observed on the wire. Used to gate signalTurnDone() so a network
  // blip or premature stream close doesn't leave a misleading "ready"
  // badge on a hidden tab when the turn is actually still running or
  // disconnected.
  let sawTurnEnd = state.sawTurnEnd;

  const syncState = () => {
    state.currentPhase = currentPhase;
    state.phaseLastEndedAt = phaseLastEndedAt;
    state.lastStandaloneEl = lastStandaloneEl;
    state.sawTurnEnd = sawTurnEnd;
  };

  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = '';

  // SSE parser — each event is `event: <name>\n data: <json>\n\n`. We
  // accumulate bytes in `buf` and consume whole frames as they arrive.
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      const parts = buf.split('\n\n');
      buf = parts.pop();
      for (const part of parts) {
        // SSE comment frames (`: keepalive`) — ignore.
        if (part.startsWith(':')) continue;
        let event = 'message';
        let data = '';
        let id = '';
        for (const line of part.split('\n')) {
          if (line.startsWith('event: ')) event = line.slice(7).trim();
          else if (line.startsWith('data: ')) data += (data ? '\n' : '') + line.slice(6);
          else if (line.startsWith('id: ')) id = line.slice(4).trim();
        }
        if (id) {
          const seq = Number.parseInt(id, 10);
          if (Number.isFinite(seq) && seq > state.lastSeq) state.lastSeq = seq;
        }
        if (!data) continue;
        let payload;
        try { payload = JSON.parse(data); } catch (e) { continue; }
      if (!firstEventFired) {
        firstEventFired = true;
        conversationHasServerState = true;
        if (onFirstEvent) {
          try { onFirstEvent(); } catch (e) {}
        }
      }
      if (event === 'block_start') {
        // Any new block_start collapses the most recent standalone
        // block (setup / notify / result / error) so the latest
        // activity stays expanded. Phase boxes manage their own
        // collapse via closePhase() below.
        if (lastStandaloneEl) {
          clearBlockActivity(lastStandaloneEl);
          lastStandaloneEl.open = false;
          lastStandaloneEl = null;
        }
        if (payload.kind === 'claude_text') {
          // A new prose chunk starts a fresh phase: collapse the prior
          // phase (if any) and open a new box with this paragraph as
          // the header. The new block's started_at is a server-stamped
          // upper bound for when the prior phase ended, so we pass it
          // through (preferring the trailing tool's ended_at when we
          // saw one).
          if (currentPhase) closePhase(currentPhase, phaseLastEndedAt || payload.started_at);
          currentPhase = openPhase(turn, payload.title, payload.started_at);
          phaseLastEndedAt = '';
          blockRefs.set(payload.id, { kind: 'phase-prose', phase: currentPhase });
        } else if (payload.kind === 'tool_use') {
          // Tool calls go inside the current phase; if no phase is
          // open (Claude jumped straight to tools without prose),
          // open one with a generic title so the visual treatment
          // stays consistent.
          if (!currentPhase) {
            currentPhase = openPhase(turn, '', payload.started_at);
            phaseLastEndedAt = '';
          }
          const el = addToolToPhase(currentPhase, payload);
          blockRefs.set(payload.id, { kind: 'tool', el, phase: currentPhase });
        } else {
          // Standalone block (setup, notify, result, error) — these
          // aren't part of a phase. Close any open phase first so the
          // step count stamps correctly, then render normally. The
          // standalone's started_at bounds the phase end on the
          // server clock when we don't have a tool's ended_at yet.
          if (currentPhase) {
            closePhase(currentPhase, phaseLastEndedAt || payload.started_at);
            currentPhase = null;
            phaseLastEndedAt = '';
          }
          const el = renderBlock(turn, {
            id: payload.id, kind: payload.kind, title: payload.title,
            body: '', status: 'streaming', started_at: payload.started_at,
          }, { open: true });
          const terminal = payload.kind === 'result' || payload.kind === 'error';
          blockRefs.set(payload.id, { kind: 'standalone', blockKind: payload.kind, terminal, el });
          lastStandaloneEl = el;
          if (payload.kind === 'notify' && payload.meta && payload.meta.tag === 'sandbox_ready') {
            refreshMetadata();
          }
          if (payload.kind === 'result' || payload.kind === 'error') {
            sawTurnEnd = true;
          }
        }
        log.scrollTop = log.scrollHeight;
      } else if (event === 'block_append') {
        const ref = blockRefs.get(payload.id);
        if (!ref) continue;
        if (ref.kind === 'phase-prose') {
          appendPhaseProse(ref.phase, payload.delta || '');
        } else if (ref.kind === 'standalone') {
          appendToBlock(ref.el, payload.delta || '');
        }
        // tool appends are dropped by the live emitter and never reach here.
      } else if (event === 'block_done') {
        const ref = blockRefs.get(payload.id);
        if (!ref) continue;
        if (ref.kind === 'phase-prose') {
          finishPhaseProse(ref.phase);
          // The phase prose's own ended_at is also a valid lower
          // bound for the phase-close instant when no tools follow.
          if (payload.ended_at && ref.phase === currentPhase) {
            phaseLastEndedAt = payload.ended_at;
          }
        } else if (ref.kind === 'tool') {
          finishBlock(ref.el, payload.status, payload.summary, payload.ended_at);
          if (payload.ended_at && ref.phase === currentPhase) {
            phaseLastEndedAt = payload.ended_at;
          }
        } else if (ref.kind === 'standalone') {
          finishBlock(ref.el, payload.status, payload.summary, payload.ended_at);
          if (!ref.terminal && payload.status !== 'error' && ref.el === lastStandaloneEl) {
            markBlockAwaitingNext(ref.el);
          }
        }
      } else if (event === 'heartbeat') {
        applyHeartbeatToActiveBlock(currentPhase, lastStandaloneEl, payload);
      }
    }
    }
  } finally {
    syncState();
  }
  // Only close activity once the terminal block has arrived. If the
  // connection drops mid-turn, the reconnect loop keeps the same render
  // state alive and asks the server for events after the last SSE id.
  if (sawTurnEnd) {
    if (currentPhase) {
      closePhase(currentPhase, phaseLastEndedAt);
      currentPhase = null;
      phaseLastEndedAt = '';
    }
    clearBlockActivity(lastStandaloneEl);
    syncState();
  }
  // GitHub-style tab indicator: if the user navigated away while
  // Hetchy was working, badge the favicon so the just-finished turn
  // is visible in their tab bar. We only paint the badge when the
  // tab is hidden — if they're actively watching the page they
  // already see the result land, and a flash-then-clear would just
  // be noise. Gated on sawTurnEnd so a dropped/aborted stream doesn't
  // produce a misleading "ready" indicator on a still-running turn.
  if (sawTurnEnd) signalTurnDone();
  return { terminal: sawTurnEnd, state, lastSeq: state.lastSeq };
}

async function send() {
  if (isRunning) return;
  const text = inp.value.trim();
  if (!text) return;
  stopRequested = false;
  inp.value = '';
  autosizeInput();
  setRunState(true);

  const taskOptions = currentTaskOptions();

  const agentChoiceApplies = conversationAgentIsMutable();
  const isFirstTurn = log.querySelectorAll('.msg').length === 0;
  addUserMsg(text);
  if (lastDetail) lastDetail.task_options = taskOptions;
  if (isFirstTurn || agentChoiceApplies) {
    setModelPickerLocked(true);
    renderPendingMetadata(text);
  }

  const payload = { text, session_id: sessionId, model: selectedModel };
  payload.validate = taskOptions[taskOptionKeys.validate];
  payload.review_code_before_push = taskOptions[taskOptionKeys.reviewBeforePush];
  payload.action_pr_checks_for_done = taskOptions[taskOptionKeys.actionPRChecks];
  if (agentChoiceApplies) {
    payload.agent_slug = selectedAgentSlug;
  }
  try {
    const res = await fetch('/chat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    if (!res.ok) {
      const body = await res.text().catch(() => '');
      throw new Error(body || ('chat request failed: ' + res.status));
    }
    // The server persists the conversation row at HandleRequest entry —
    // before sandbox creation begins. By the time the first SSE event
    // reaches us the row is committed, so triggering a sidebar refresh on
    // first event surfaces the new chat immediately rather than waiting
    // for the agent to finish (which can take minutes).
    await streamTurnWithReconnect(res, {
      onFirstEvent: isFirstTurn ? () => { loadSidebar(); refreshMetadata(); } : null
    });
  } finally {
    stopRequested = false;
    setRunState(false);
    inp.focus();
    // Refresh the sidebar after every turn so the row's title (derived
    // server-side from the first user message) reflects the just-completed
    // turn. Ordering is by created_at DESC, so existing rows keep their slot
    // and a brand-new chat appears at the top of page 0 on the first reload.
    loadSidebar();
    // Metadata may have changed too — branch + PR URL only land at the
    // end of the first turn, and the agent can re-base subsequent turns.
    refreshMetadata();
  }
}

async function stopRun() {
  if (!isRunning || isStopping) return;
  stopRequested = true;
  setRunState(true, true);
  try {
    const res = await fetch('/chat/cancel', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session_id: sessionId })
    });
    if (!res.ok) {
      stopRequested = false;
      setRunState(res.status !== 404, false);
      return;
    }
    hideToast('stream-disconnected');
    await loadHistory();
  } catch (e) {
    stopRequested = false;
    setRunState(true, false);
  }
}

// Sidebar user filter — populate the <select> with org members and wire up
// the change handler to refresh the conversation list.
// addOption is a small DOM-API helper that avoids innerHTML
// concatenation when the value is a server-supplied identifier — the
// option's value/text never crosses an HTML parser, so escaping bugs
// in user_id can't break out into attribute context.
function addOption(sel, value, text) {
  const opt = document.createElement('option');
  opt.value = value;
  opt.textContent = text;
  sel.appendChild(opt);
}

async function loadMembers() {
  const sel = document.getElementById('user-filter');
  try {
    const res = await fetch('/api/members', { headers: { 'Accept': 'application/json' } });
    if (!res.ok) throw new Error('members fetch failed: ' + res.status);
    const members = (await res.json()) || [];
    sel.innerHTML = '';
    addOption(sel, '', 'All');
    for (const m of members) {
      const label = m.display_name || m.email;
      // Cache the display name so the metadata sidebar can render
      // "Created by First Last" instead of the WorkOS user id.
      memberById.set(m.user_id, label || m.user_id);
      let text;
      if (m.user_id === currentUserID) {
        text = label ? 'Me (' + label + ')' : 'Me';
      } else {
        text = label || m.user_id;
      }
      addOption(sel, m.user_id, text);
    }
    // Members loaded after the metadata may have rendered with raw
    // creator ids — re-paint with the freshly resolved names.
    if (lastDetail) renderMetadata(lastDetail);
  } catch (e) {
    // Network or server error: render a minimal dropdown so the label
    // matches the actual filter state. Without "Me" here, the dropdown
    // would read "All" while the sidebar is in fact scoped to the
    // current user (userFilter defaults to currentUserID), which looks
    // like a bug to anyone trying to widen the view.
    sel.innerHTML = '';
    addOption(sel, '', 'All');
    if (currentUserID) {
      addOption(sel, currentUserID, 'Me');
    }
  }
  // Honor the persisted filter, but if it points at someone who is no
  // longer in the org (or who was never an option here, e.g. typo in
  // localStorage), fall back to the current user. Otherwise the
  // dropdown silently shows blank "All" while loadSidebar() still
  // queries with the stale id and returns an empty list.
  const known = Array.from(sel.options).some(o => o.value === userFilter);
  if (!known) {
    userFilter = currentUserID;
    try { localStorage.setItem(userFilterStorageKey, userFilter); } catch (e) {}
  }
  sel.value = userFilter;
}

document.getElementById('user-filter').addEventListener('change', function () {
  userFilter = this.value;
  try { localStorage.setItem(userFilterStorageKey, userFilter); } catch (e) {}
  loadSidebar();
});

// Search box — debounce keystrokes so a fast typist doesn't issue a
// fetch per character. 200ms feels instant but coalesces a normal
// burst of typing into one request. The 'search' event also fires
// when the browser's built-in clear-X is clicked, which lands as an
// empty value — handled by the same path.
const chatSearchInput = document.getElementById('chat-search');
function applySearchValue() {
  const next = chatSearchInput.value.trim();
  if (next === searchQuery) return;
  searchQuery = next;
  loadSidebar();
}
chatSearchInput.addEventListener('input', () => {
  if (searchDebounce) clearTimeout(searchDebounce);
  searchDebounce = setTimeout(applySearchValue, 200);
});
// Native search-clear button + Enter both fire 'search'; flush
// immediately rather than waiting for the debounce.
chatSearchInput.addEventListener('search', () => {
  if (searchDebounce) { clearTimeout(searchDebounce); searchDebounce = null; }
  applySearchValue();
});

// Search toggle — expand the search input over the user filter on click;
// collapse (and clear) on second click or Escape.
const searchToggle = document.getElementById('search-toggle');
const sidebarFilter = document.getElementById('sidebar-filter');
function openSearch() {
  sidebarFilter.classList.add('is-searching');
  searchToggle.setAttribute('aria-expanded', 'true');
  chatSearchInput.focus();
}
function closeSearch() {
  sidebarFilter.classList.remove('is-searching');
  searchToggle.setAttribute('aria-expanded', 'false');
  if (chatSearchInput.value) {
    chatSearchInput.value = '';
    applySearchValue();
  }
  searchToggle.focus();
}
searchToggle.addEventListener('click', () => {
  if (sidebarFilter.classList.contains('is-searching')) closeSearch();
  else openSearch();
});
chatSearchInput.addEventListener('keydown', e => {
  if (e.key === 'Escape') { e.stopPropagation(); closeSearch(); }
});

document.getElementById('chat-list-more-btn').addEventListener('click', loadMoreSidebar);

// Metadata sidebar visibility — persisted across reloads so the user's
// preference sticks. The class is set on <html> by the head-bootstrap
// (no FOUC); this block keeps the button's aria-pressed in sync and
// wires the click handler. Default = visible on desktop; hidden on
// mobile-first-visit (the head bootstrap defaults the class on for
// small viewports so the slide-in panel doesn't cover the chat).
const metaHiddenStorageKey = 'hetchy.metaHidden';
// Forward references for the mobile sidebar overlay — the metaHidden
// path below calls syncOverlayScrim() which reads overlayScrim, and
// the meta-toggle click handler calls setSidebarOpen(). Both are
// defined further down; declaring them up here keeps the TDZ happy.
const sidebarToggleBtn = document.getElementById('sidebar-toggle');
const overlayScrim = document.getElementById('overlay-scrim');
const mobileSidebarQuery = window.matchMedia('(max-width: 768px)');
function applyMetaHidden(hidden) {
  document.documentElement.classList.toggle('meta-hidden', hidden);
  const btn = document.getElementById('meta-toggle');
  btn.setAttribute('aria-pressed', String(!hidden));
  syncOverlayScrim();
}
// The bootstrap script in <head> already added the class if needed —
// re-read the same source here so aria-pressed matches.
applyMetaHidden(document.documentElement.classList.contains('meta-hidden'));
document.getElementById('meta-toggle').addEventListener('click', () => {
  const nowHidden = !document.documentElement.classList.contains('meta-hidden');
  applyMetaHidden(nowHidden);
  try { localStorage.setItem(metaHiddenStorageKey, nowHidden ? '1' : '0'); } catch (e) {}
  // Opening the meta panel on mobile should close the chat-list panel
  // (and vice versa) so only one overlay covers the screen at a time.
  if (!nowHidden) setSidebarOpen(false);
});

// Mobile sidebar overlay — toggled by #sidebar-toggle in #main-top.
// Adds `html.sidebar-open` to slide the existing chat-list sidebar in
// from the left. The class is meaningless on desktop (CSS only acts
// on it inside the mobile @media block) but we still clear it on
// resize so a stale state doesn't leak across breakpoints.
function setSidebarOpen(open) {
  document.documentElement.classList.toggle('sidebar-open', !!open);
  sidebarToggleBtn.setAttribute('aria-expanded', String(!!open));
  syncOverlayScrim();
}
function syncOverlayScrim() {
  // The scrim is visible whenever a phone-width overlay is open.
  // On desktop the @media display:none rule keeps it hidden regardless
  // of the [hidden] attribute. Keeping the attribute in sync avoids
  // catching pointer events when neither overlay is active.
  const anyOpen =
    document.documentElement.classList.contains('sidebar-open') ||
    !document.documentElement.classList.contains('meta-hidden');
  overlayScrim.hidden = !anyOpen;
}
sidebarToggleBtn.addEventListener('click', e => {
  e.stopPropagation();
  closeToolsPopover();
  closeModelPopover();
  closeAgentPopover();
  setUserMenuOpen(false);
  const open = !document.documentElement.classList.contains('sidebar-open');
  setSidebarOpen(open);
  // Same one-overlay-at-a-time rule as the meta toggle above.
  if (open && !document.documentElement.classList.contains('meta-hidden')) {
    applyMetaHidden(true);
    try { localStorage.setItem(metaHiddenStorageKey, '1'); } catch (e) {}
  }
});
overlayScrim.addEventListener('click', () => {
  setSidebarOpen(false);
  if (!document.documentElement.classList.contains('meta-hidden')) {
    applyMetaHidden(true);
    try { localStorage.setItem(metaHiddenStorageKey, '1'); } catch (e) {}
  }
});
// Tapping a chat in the sidebar navigates — close the overlay so the
// user lands on the chat instead of staring at the same list.
document.getElementById('chat-list').addEventListener('click', e => {
  if (e.target.closest('a.chat-item') && mobileSidebarQuery.matches) {
    setSidebarOpen(false);
  }
});
document.addEventListener('keydown', e => {
  if (e.key === 'Escape' &&
      document.documentElement.classList.contains('sidebar-open')) {
    setSidebarOpen(false);
  }
});
// Clear the overlay state when crossing past the mobile breakpoint so
// rotating a tablet doesn't leave the desktop layout with a stuck
// "open" class.
mobileSidebarQuery.addEventListener('change', e => {
  if (!e.matches && document.documentElement.classList.contains('sidebar-open')) {
    setSidebarOpen(false);
  }
});
syncOverlayScrim();
// Avatar dropdown — toggle on click, close on outside-click or Escape.
const userMenuBtn = document.getElementById('user-menu-btn');
const userMenuDrop = document.getElementById('user-menu-dropdown');
function setUserMenuOpen(open) {
  userMenuDrop.hidden = !open;
  userMenuBtn.setAttribute('aria-expanded', String(open));
}
userMenuBtn.addEventListener('click', e => {
  e.stopPropagation();
  setUserMenuOpen(userMenuDrop.hidden);
});
document.addEventListener('click', e => {
  if (!userMenuDrop.hidden && !userMenuDrop.contains(e.target)) setUserMenuOpen(false);
});
document.addEventListener('keydown', e => {
  if (e.key === 'Escape' && !userMenuDrop.hidden) {
    setUserMenuOpen(false);
    userMenuBtn.focus();
  }
});

// init paints the sidebar, populates the user filter, and renders the
// chat. On a reload, it first probes /chat/stream to see if a turn
// is currently in flight: if so, it loads persisted prior turns AND
// attaches to the live SSE stream so the user sees the in-progress
// turn updating in real time. If no turn is in flight, it falls
// back to rendering the persisted snapshot.
async function init() {
  populateModelPicker();
  await loadAgents();
  loadMembers();
  loadSidebar();

  if (isFreshChat) {
    showEmptyState();
    return;
  }

  let liveRes = null;
  let reattachPending = false;
  try {
    const r = await openChatStream();
    if (r.ok) {
      liveRes = r;
    } else if (r.status === 409) {
      reattachPending = true;
      if (r.body) {
        try { await r.body.cancel(); } catch (e) {}
      }
    } else if (r.body) {
      // 404 / other — drain so the connection releases promptly.
      try { await r.body.cancel(); } catch (e) {}
    }
  } catch (e) {
    // Network blip — fall back to the persisted snapshot.
  }

  await loadHistory({ skipLastBotResponse: !!liveRes || reattachPending });
  if (liveRes || reattachPending) {
    setRunState(true);
    try {
      await streamTurnWithReconnect(liveRes, {
        waitingTitle: 'Reconnecting',
        waitingBody: 'Waiting for the active run to resume after the page reconnected.'
      });
    } finally {
      stopRequested = false;
      setRunState(false);
      inp.focus();
      // Refresh the sidebar so the just-completed turn's metadata
      // (title, ordering) lines up with what the server saved.
      loadSidebar();
      // Branch + PR URL only land at end-of-turn — re-fetch the detail
      // so the right-hand metadata panel picks them up.
      refreshMetadata();
    }
  }
}

init();
