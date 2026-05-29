// init paints the sidebar, populates the user filter, and renders the
// chat. On a reload, it first probes the conversation events API to see if a turn
// is currently in flight: if so, it loads persisted prior turns AND
// attaches to the live SSE stream so the user sees the in-progress
// turn updating in real time. If no turn is in flight, it falls
// back to rendering the persisted snapshot.
async function init() {
  populateModelPicker();
  populateRepoPicker();
  updateRepoButton();
  await Promise.all([loadAgents(), loadRepos('')]);
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

// pageshow fires on every visibility transition, including bfcache
// restores. When the browser restores a chat page from bfcache (Chrome /
// Safari do this on back/forward navigation, even from a sidebar link
// click that came from another tab), the original DOM and JS state come
// back unchanged — including a stale lastDetail that may have lost the
// attachments + PR URL because they hadn't been persisted yet on the
// initial render. event.persisted is true only on a bfcache restore, so
// we re-fetch the conversation detail and repaint the right-hand sidebar.
window.addEventListener('pageshow', (event) => {
  if (!event.persisted) return;
  refreshMetadata();
});
