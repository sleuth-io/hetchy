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
  scrollLogToBottomIfPinned();
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
    scrollLogToBottomIfPinned();
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
    scrollLogToBottomIfPinned();
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
  // Reset the scroll pin so a fresh chat starts in the "follow new
  // content" state regardless of where the user had scrolled in the
  // chat we just navigated away from.
  stickToBottom = true;
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

