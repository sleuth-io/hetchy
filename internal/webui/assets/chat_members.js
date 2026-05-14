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
