      // Delete-bootstrap flow:
      //   1. Confirm dialog explains the side-effect (a future re-run, not
      //      an immediate one) so users know the cost is paid lazily.
      //   2. DELETE /api/v1/repo-bootstrap drops the saved spec; the server
      //      handler is idempotent and never deletes per-repo secrets.
      //   3. On success, swap the card's right-hand side to the
      //      "not bootstrapped" empty state in-place and show a toast —
      //      no full reload, the rest of the page stays put.
      (function () {
        var toast = document.getElementById('repo-toast');
        var toastTimer = null;
        function showToast(msg, level) {
          toast.textContent = msg;
          toast.classList.remove('repo-toast-err');
          if (level === 'error') toast.classList.add('repo-toast-err');
          toast.hidden = false;
          if (toastTimer) clearTimeout(toastTimer);
          toastTimer = setTimeout(function () { toast.hidden = true; }, 6000);
        }

        // Set / replace secret value — PUT /api/v1/repo-secrets carries
        // the owner/name/secret + value. Same endpoint serves both
        // first-time set and replace; the "filled" state visible in
        // the row only changes the wording. On success we flip the
        // row in place to filled (or re-render its save state) so the
        // user gets immediate feedback without a full reload.
        function flipSecretRow(details, filled) {
          if (!details) return;
          details.open = false;
          details.classList.toggle('is-filled', !!filled);
          var stateEl = details.querySelector('.repo-secret-state');
          if (stateEl) {
            stateEl.classList.toggle('is-set', !!filled);
            stateEl.classList.toggle('is-unset', !filled);
            stateEl.textContent = filled ? 'set' : 'not set';
          }
          var form = details.querySelector('.repo-secret-form');
          if (form) {
            form.dataset.filled = filled ? '1' : '0';
            form.reset();
            var input = form.querySelector('input[name=value]');
            if (input) input.placeholder = filled
              ? 'Paste a new value to replace…'
              : 'Paste value…';
            var save = form.querySelector('.repo-secret-save');
            if (save) { save.disabled = false; save.textContent = filled ? 'Replace' : 'Save'; }
            // Clear-value link is only meaningful for filled rows;
            // toggle visibility so a row that was just cleared can't
            // be re-cleared until it has been re-set.
            var clear = form.querySelector('.repo-secret-clear');
            if (clear) clear.hidden = !filled;
            var status = form.querySelector('.repo-secret-status');
            if (status) { status.hidden = true; status.classList.remove('is-error'); status.textContent = ''; }
          }
        }

        // Clear-value dialog — same single-shared-instance pattern as
        // the delete-bootstrap one. The form's Clear button stamps
        // owner / name / secret + a back-pointer onto the dialog,
        // calls showModal(); confirm runs DELETE /api/v1/repo-secrets.
        var clearDlg     = document.getElementById('clear-secret-dialog');
        var clearDlgName = document.getElementById('clear-secret-name');
        var clearDlgSlug = document.getElementById('clear-secret-slug');
        var clearConfirm = document.getElementById('clear-secret-confirm');
        var clearCancel  = document.getElementById('clear-secret-cancel');
        var pendingClear = null;

        if (clearCancel) clearCancel.addEventListener('click', function () { clearDlg.close(); pendingClear = null; });
        if (clearDlg) clearDlg.addEventListener('close', function () { pendingClear = null; });
        if (clearConfirm) clearConfirm.addEventListener('click', async function () {
          if (!pendingClear) { clearDlg.close(); return; }
          var owner   = pendingClear.owner;
          var name    = pendingClear.name;
          var secret  = pendingClear.secret;
          var details = pendingClear.details;
          clearConfirm.disabled = true;
          clearConfirm.textContent = 'Clearing…';
          try {
            var qs  = '?owner=' + encodeURIComponent(owner)
                    + '&name=' + encodeURIComponent(name)
                    + '&secret_name=' + encodeURIComponent(secret);
            var res = await fetch('/api/v1/repo-secrets' + qs, { method: 'DELETE' });
            if (!res.ok) {
              var body = await res.text();
              showToast('Could not clear ' + secret + ': ' + res.status + ' ' + (body || res.statusText), 'error');
            } else {
              showToast('Cleared ' + secret + ' on ' + owner + '/' + name + '.');
              flipSecretRow(details, false);
            }
          } catch (e) {
            showToast('Could not clear ' + secret + ': ' + e, 'error');
          } finally {
            clearConfirm.disabled = false;
            clearConfirm.textContent = 'Clear value';
            clearDlg.close();
            pendingClear = null;
          }
        });

        document.querySelectorAll('.repo-secret-form').forEach(function (form) {
          var details = form.closest('details');
          var statusEl = form.querySelector('.repo-secret-status');
          var cancel   = form.querySelector('.repo-secret-cancel');
          var clear    = form.querySelector('.repo-secret-clear');
          if (cancel) {
            cancel.addEventListener('click', function () {
              if (details) details.open = false;
              form.reset();
              if (statusEl) { statusEl.hidden = true; statusEl.classList.remove('is-error'); }
            });
          }
          if (clear) {
            clear.addEventListener('click', function () {
              pendingClear = {
                owner:   form.getAttribute('data-owner'),
                name:    form.getAttribute('data-name'),
                secret:  form.getAttribute('data-secret'),
                details: details,
              };
              if (clearDlgName) clearDlgName.textContent = pendingClear.secret;
              if (clearDlgSlug) clearDlgSlug.textContent = pendingClear.owner + '/' + pendingClear.name;
              if (typeof clearDlg.showModal === 'function') {
                clearDlg.showModal();
              } else if (window.confirm('Clear ' + pendingClear.secret + '?')) {
                clearConfirm.click();
              }
            });
          }
          form.addEventListener('submit', async function (e) {
            e.preventDefault();
            var owner  = form.getAttribute('data-owner');
            var name   = form.getAttribute('data-name');
            var secret = form.getAttribute('data-secret');
            var input  = form.querySelector('input[name=value]');
            var value  = input ? input.value : '';
            var save   = form.querySelector('.repo-secret-save');
            var wasFilled = form.getAttribute('data-filled') === '1';
            if (!value) return;
            if (save) { save.disabled = true; save.textContent = 'Saving…'; }
            if (statusEl) { statusEl.hidden = true; statusEl.classList.remove('is-error'); }
            try {
              var res = await fetch('/api/v1/repo-secrets', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ owner: owner, name: name, secret_name: secret, value: value }),
              });
              if (!res.ok) {
                var body = await res.text();
                if (statusEl) {
                  statusEl.textContent = 'Save failed: ' + res.status + ' ' + (body || res.statusText);
                  statusEl.classList.add('is-error');
                  statusEl.hidden = false;
                }
                if (save) { save.disabled = false; save.textContent = wasFilled ? 'Replace' : 'Save'; }
                return;
              }
              showToast((wasFilled ? 'Replaced ' : 'Saved ') + secret + ' on ' + owner + '/' + name + '.');
              flipSecretRow(details, true);
            } catch (err) {
              if (statusEl) {
                statusEl.textContent = 'Save failed: ' + err;
                statusEl.classList.add('is-error');
                statusEl.hidden = false;
              }
              if (save) { save.disabled = false; save.textContent = wasFilled ? 'Replace' : 'Save'; }
            }
          });
        });

        // Delete bootstrap flow: clicking the per-card button stamps
        // the owner/name + a back-pointer to the originating button on
        // the shared <dialog>, then calls showModal(). The dialog's
        // own confirm handler reads those off and runs the DELETE +
        // in-place card swap. One dialog instance serves every card.
        var dlg          = document.getElementById('delete-bootstrap-dialog');
        var dlgSlugEl    = document.getElementById('delete-bootstrap-slug');
        var dlgConfirm   = document.getElementById('delete-bootstrap-confirm');
        var dlgCancel    = document.getElementById('delete-bootstrap-cancel');
        var pendingBtn   = null;

        function openDeleteDialog(btn) {
          pendingBtn = btn;
          var owner = btn.getAttribute('data-owner');
          var name  = btn.getAttribute('data-name');
          var slug  = btn.getAttribute('data-slug');
          dlg.dataset.owner = owner;
          dlg.dataset.name  = name;
          dlg.dataset.slug  = slug;
          if (dlgSlugEl) dlgSlugEl.textContent = slug;
          dlgConfirm.disabled = false;
          dlgConfirm.textContent = 'Delete bootstrap';
          if (typeof dlg.showModal === 'function') {
            dlg.showModal();
          } else {
            // Defensive fallback for very old browsers without
            // <dialog> support — fall through to the original
            // confirm() rather than break the flow entirely.
            if (window.confirm('Delete bootstrap spec for ' + slug + '? Next task will re-bootstrap from scratch.')) {
              runDelete();
            }
          }
        }

        if (dlgCancel) dlgCancel.addEventListener('click', function () { dlg.close(); pendingBtn = null; });
        // Pressing Esc / clicking the backdrop also closes the dialog
        // — clear pendingBtn so a stale reference can't fire later.
        if (dlg) dlg.addEventListener('close', function () { pendingBtn = null; });

        async function runDelete() {
          var owner = dlg.dataset.owner;
          var name  = dlg.dataset.name;
          var slug  = dlg.dataset.slug;
          var btn   = pendingBtn;
          dlgConfirm.disabled = true;
          dlgConfirm.textContent = 'Deleting…';
          if (btn) {
            btn.disabled = true;
            btn.textContent = 'Deleting…';
          }
          try {
            var qs  = '?owner=' + encodeURIComponent(owner) + '&name=' + encodeURIComponent(name);
            var res = await fetch('/api/v1/repo-bootstrap' + qs, { method: 'DELETE' });
            if (!res.ok) {
              var body = await res.text();
              showToast('Could not delete: ' + res.status + ' ' + (body || res.statusText), 'error');
              dlg.close();
              if (btn) { btn.disabled = false; btn.textContent = 'Delete bootstrap'; }
              return;
            }
            showToast('Bootstrap deleted for ' + slug + '. The next task on this repo will re-bootstrap from scratch.');
            dlg.close();
            // In-place swap: turn this card into the "not bootstrapped" form.
            var card = btn ? btn.closest('.repo-card') : null;
            if (card) {
              var head = card.querySelector('.repo-card-head');
              if (head) {
                head.querySelectorAll('.tag, .repo-card-kind').forEach(function (el) { el.remove(); });
                var pending = document.createElement('span');
                pending.className = 'tag tag-pending';
                pending.textContent = 'not bootstrapped';
                head.appendChild(pending);
              }
              card.querySelectorAll('.repo-card-cols, .repo-card-allset').forEach(function (el) { el.remove(); });
              if (!card.querySelector('.repo-card-empty')) {
                var empty = document.createElement('p');
                empty.className = 'repo-card-empty';
                empty.textContent = 'The first task you start against this repo will trigger an automatic bootstrap. Expect a few extra minutes on that first run; subsequent tasks reuse the cached spec.';
                card.insertBefore(empty, card.querySelector('.repo-card-foot'));
              }
              var foot = card.querySelector('.repo-card-foot');
              if (foot) foot.innerHTML = '';
            }
          } catch (e) {
            showToast('Could not delete: ' + e, 'error');
            dlg.close();
            if (btn) { btn.disabled = false; btn.textContent = 'Delete bootstrap'; }
          } finally {
            pendingBtn = null;
          }
        }
        if (dlgConfirm) dlgConfirm.addEventListener('click', runDelete);

        document.querySelectorAll('.delete-bootstrap-btn').forEach(function (btn) {
          btn.addEventListener('click', function () { openDeleteDialog(btn); });
        });
      })();
