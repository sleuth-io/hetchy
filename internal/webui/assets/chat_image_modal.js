// Image attachment modal — opened from any element tagged with
// data-image-modal-url anywhere on the page (chat messages, sidebar
// details). The trigger handler lives at document level so dynamically
// rebuilt subtrees (e.g. renderMetadata wiping #meta-content) keep
// working without each renderer rewiring its own listeners.

function isImageAttachmentMimeType(contentType) {
  const ct = String(contentType || '').toLowerCase().trim();
  return ct.startsWith('image/');
}

// Blob URLs minted for in-flight uploads keep the underlying File alive
// until URL.revokeObjectURL runs. We hold them in a Set and revoke when
// the chat log is rebuilt from authoritative server state (loadHistory
// clears #log) or on page unload, since at those points the original
// chat-message bubble — the only place the blob URL is referenced — is
// no longer reachable.
const trackedPreviewBlobURLs = new Set();

function trackPreviewBlobURL(url) {
  if (url && url.startsWith('blob:')) trackedPreviewBlobURLs.add(url);
  return url;
}

function revokeTrackedPreviewBlobURLs() {
  for (const url of trackedPreviewBlobURLs) URL.revokeObjectURL(url);
  trackedPreviewBlobURLs.clear();
}

window.addEventListener('beforeunload', revokeTrackedPreviewBlobURLs);

// activeImageModalClose is the teardown closure for the currently
// mounted modal. We call it before opening a new one so the previous
// instance's keydown listener doesn't leak when a second image is
// clicked while the first modal is still up.
let activeImageModalClose = null;

function openImageModal(url, filename, trigger) {
  if (!url) return;
  if (activeImageModalClose) activeImageModalClose();

  const overlay = document.createElement('div');
  overlay.id = 'image-modal-overlay';
  overlay.className = 'image-modal-overlay';

  const titleID = 'image-modal-title';
  overlay.innerHTML =
    '<div class="image-modal" role="dialog" aria-modal="true"'
    +   ' aria-labelledby="' + titleID + '" tabindex="-1">'
    +   '<div class="image-modal-head">'
    +     '<h2 class="image-modal-title" id="' + titleID + '"></h2>'
    +     '<button type="button" class="image-modal-close"'
    +       ' aria-label="Close">&times;</button>'
    +   '</div>'
    +   '<div class="image-modal-body">'
    +     '<img class="image-modal-img" alt="">'
    +   '</div>'
    + '</div>';

  const name = String(filename || 'image');
  overlay.querySelector('.image-modal-title').textContent = name;
  const img = overlay.querySelector('.image-modal-img');
  img.alt = name;
  img.src = url;

  const dialog = overlay.querySelector('.image-modal');
  const lastFocus = document.activeElement;
  const close = () => {
    document.removeEventListener('keydown', onKey);
    overlay.remove();
    if (activeImageModalClose === close) activeImageModalClose = null;
    // The trigger may have been re-rendered out from under us (the
    // sidebar swaps #meta-content wholesale), so restore focus only
    // when the original element is still on the page.
    if (lastFocus && typeof lastFocus.focus === 'function'
        && document.contains(lastFocus)) {
      lastFocus.focus();
    }
  };
  activeImageModalClose = close;
  const onKey = (e) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      close();
      return;
    }
    if (e.key !== 'Tab') return;
    // Focus trap: cycle between the close button (the only focusable
    // descendant) so Tab doesn't escape to the chat behind the modal.
    const focusable = Array.from(dialog.querySelectorAll('button:not([disabled])'));
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    const active = document.activeElement;
    if (e.shiftKey && (active === first || !dialog.contains(active))) {
      e.preventDefault(); last.focus();
    } else if (!e.shiftKey && (active === last || !dialog.contains(active))) {
      e.preventDefault(); first.focus();
    }
  };
  document.addEventListener('keydown', onKey);
  overlay.addEventListener('click', (e) => {
    if (e.target === overlay) close();
  });
  overlay.querySelector('.image-modal-close').addEventListener('click', close);

  const mountRoot = trigger && typeof trigger.closest === 'function'
    ? (trigger.closest('dialog[open]') || document.body)
    : document.body;
  mountRoot.appendChild(overlay);
  overlay.querySelector('.image-modal-close').focus();
}

// Global click delegation: any element with data-image-modal-url
// (anchor, button, span) opens the modal when activated. preventDefault
// keeps a wrapping <a download> from also kicking off a download in
// parallel. Modified clicks (Ctrl/Cmd/Shift/Alt) and middle/right
// clicks fall through so power users can still open the underlying
// URL in a new tab or copy it.
document.addEventListener('click', (e) => {
  if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  const trigger = e.target.closest('[data-image-modal-url]');
  if (!trigger) return;
  e.preventDefault();
  openImageModal(trigger.dataset.imageModalUrl, trigger.dataset.imageModalName || '', trigger);
});
