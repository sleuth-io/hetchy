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
    // referenced block, otherwise scroll to the bottom so the user sees
    // the end of the conversation rather than the top.
    if (window.location.hash && window.location.hash.startsWith('#block-')) {
      const target = document.getElementById(window.location.hash.slice(1));
      if (target) {
        target.open = true;
        target.scrollIntoView({ behavior: 'smooth', block: 'center' });
      }
    } else {
      log.scrollTop = log.scrollHeight;
    }
  } catch (e) {
    // Leave the log empty on error; the user can still type a message.
  }
}
