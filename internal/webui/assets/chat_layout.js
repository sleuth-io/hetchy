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

