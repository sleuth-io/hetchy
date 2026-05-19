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
    if (!c.id) continue;
    const wrap = document.createElement('div');
    const conversationID = c.id;
    const isActive = conversationID === sessionId;
    wrap.className = 'chat-item-wrap' + (isActive ? ' wrap-active' : '');

    const a = document.createElement('a');
    a.className = 'chat-item' + (isActive ? ' active' : '');
    a.href = '/?session=' + encodeURIComponent(conversationID);
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
      toggleChatMenu(menuBtn, conversationID, c.title);
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
    res = await fetch('/api/v1/conversations/' + encodeURIComponent(threadId), {
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
    res = await fetch('/api/v1/conversations/' + encodeURIComponent(threadId), { method: 'DELETE' });
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

// fetchSidebarPage hits /api/v1/conversations with the current filter +
// search + offset. Returns the JSON array on success, null on
// transport failure (the caller renders a generic error in that case).
async function fetchSidebarPage(offset) {
  const params = new URLSearchParams();
  if (userFilter)  params.set('user', userFilter);
  if (searchQuery) params.set('q', searchQuery);
  params.set('limit', String(SIDEBAR_PAGE_SIZE));
  params.set('offset', String(offset));
  const res = await fetch('/api/v1/conversations?' + params.toString(),
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
