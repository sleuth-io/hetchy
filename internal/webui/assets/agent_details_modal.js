// Agent / skill detail modal. A single <dialog> is reused across every
// AGENTS.md link and every skill chip; the click handler stashes the
// requested source on the modal and kicks off a fetch.
(function () {
  const modal = document.getElementById('modal-agent-details');
  if (!modal) return;
  const title = modal.querySelector('#agent-details-title');
  const meta = modal.querySelector('[data-agent-details-meta]');
  const loading = modal.querySelector('[data-agent-details-loading]');
  const errorEl = modal.querySelector('[data-agent-details-error]');
  const rendered = modal.querySelector('[data-agent-details-rendered]');
  const raw = modal.querySelector('[data-agent-details-raw]');
  const sidebar = modal.querySelector('[data-agent-details-sidebar]');
  const fileList = modal.querySelector('[data-agent-details-file-list]');
  let currentSkill = null;
  let requestSeq = 0;

  function setState(state) {
    loading.hidden = state !== 'loading';
    errorEl.hidden = state !== 'error';
    rendered.hidden = state !== 'rendered';
    raw.hidden = state !== 'raw';
  }

  function showError(message) {
    errorEl.textContent = message;
    setState('error');
  }

  function renderFileList(files, activePath) {
    fileList.innerHTML = '';
    files.forEach(file => {
      const li = document.createElement('li');
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'agent-details-file-item' + (file.path === activePath ? ' is-active' : '');
      btn.textContent = file.path;
      btn.addEventListener('click', () => {
        if (file.path === activePath) return;
        loadSkill(currentSkill, file.path);
      });
      li.appendChild(btn);
      fileList.appendChild(li);
    });
  }

  function renderDoc(payload, isSkill) {
    if (isSkill) {
      let fileMeta = payload.path || '';
      if (payload.is_binary) fileMeta += ' - binary';
      if (payload.truncated) fileMeta += ' - truncated';
      meta.textContent = fileMeta;
      meta.hidden = !fileMeta;
    } else {
      meta.textContent = payload.file_name || '';
      meta.hidden = !payload.file_name;
    }
    if (payload.is_binary) {
      rendered.innerHTML = '';
      raw.textContent = 'Binary file - preview not available.';
      setState('raw');
      return;
    }
    if (payload.content_html) {
      rendered.innerHTML = payload.content_html;
      setState('rendered');
      return;
    }
    raw.textContent = payload.content_md || '';
    setState('raw');
  }

  function openModal() {
    if (typeof modal.showModal === 'function' && !modal.open) {
      modal.showModal();
    }
  }

  async function loadAgent(slug, displayName) {
    const seq = ++requestSeq;
    currentSkill = null;
    sidebar.hidden = true;
    fileList.innerHTML = '';
    title.textContent = (displayName || slug) + ' - AGENTS.md';
    meta.hidden = true;
    setState('loading');
    openModal();
    try {
      const url = '/settings/org/agent-doc?slug=' + encodeURIComponent(slug);
      const res = await fetch(url, { credentials: 'same-origin', headers: { Accept: 'application/json' } });
      if (seq !== requestSeq) return;
      if (!res.ok) {
        showError('Failed to load agent file (' + res.status + ').');
        return;
      }
      const payload = await res.json();
      if (seq !== requestSeq) return;
      title.textContent = (payload.display_name || displayName || slug) + ' - AGENTS.md';
      renderDoc(payload, false);
    } catch (err) {
      if (seq === requestSeq) showError('Failed to load agent file.');
    }
  }

  async function loadSkill(name, path) {
    const seq = ++requestSeq;
    currentSkill = name;
    title.textContent = name;
    meta.hidden = true;
    fileList.innerHTML = '';
    setState('loading');
    sidebar.hidden = false;
    openModal();
    try {
      const params = new URLSearchParams({ name });
      if (path) params.set('path', path);
      const res = await fetch('/settings/org/skill-doc?' + params.toString(), {
        credentials: 'same-origin',
        headers: { Accept: 'application/json' }
      });
      if (seq !== requestSeq) return;
      if (!res.ok) {
        showError('Failed to load skill (' + res.status + ').');
        return;
      }
      const payload = await res.json();
      if (seq !== requestSeq) return;
      title.textContent = (payload.display_name || payload.name || name);
      renderFileList(payload.files || [], payload.path);
      renderDoc(payload, true);
    } catch (err) {
      if (seq === requestSeq) showError('Failed to load skill.');
    }
  }

  document.querySelectorAll('[data-open-agent-doc]').forEach(btn => {
    btn.addEventListener('click', e => {
      e.preventDefault();
      loadAgent(btn.dataset.openAgentDoc, btn.dataset.agentDisplayName || '');
    });
  });
  document.querySelectorAll('[data-open-skill-doc]').forEach(btn => {
    btn.addEventListener('click', e => {
      e.preventDefault();
      e.stopPropagation();
      loadSkill(btn.dataset.openSkillDoc, '');
    });
  });
  modal.addEventListener('close', () => {
    currentSkill = null;
    requestSeq++;
    fileList.innerHTML = '';
    sidebar.hidden = true;
    rendered.innerHTML = '';
    raw.textContent = '';
    errorEl.textContent = '';
    meta.textContent = '';
    meta.hidden = true;
    setState('loading');
  });
  modal.addEventListener('click', e => {
    if (e.target === modal) modal.close();
  });
})();
