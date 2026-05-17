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
          if (payload.kind === 'notify' && payload.meta) {
            if (payload.meta.tag === 'sandbox_ready') {
              refreshMetadata();
            }
            if (Array.isArray(payload.meta.sx_skills)) {
              updateLiveSXSkills(payload.meta.sx_skills);
            }
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
  const repoChoiceApplies = conversationRepoIsMutable();
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
  // Only send `repository` on turns where the conversation hasn't
  // locked one in yet. Sending it on follow-ups would be a no-op
  // server-side (HandleRequest pins the repo at sandbox creation), but
  // the explicit guard keeps the wire payload honest about what the
  // server will actually use.
  if (repoChoiceApplies && selectedRepoSlug) {
    payload.repository = selectedRepoSlug;
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
