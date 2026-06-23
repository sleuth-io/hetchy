  var agentSkillPreviewCount = 10;
  var agentAssetsModalClose = null;
  var agentDocumentModalClose = null;

  function agentSkillGroups(agent) {
    const direct = [];
    const inherited = [];
    const seen = new Set();
    const directKeys = new Set();
    for (const name of Array.isArray(agent && agent.skills) ? agent.skills : []) {
      const key = skillKey(name);
      if (!key || seen.has(key)) continue;
      seen.add(key);
      directKeys.add(key);
      direct.push({ name, label: skillDisplayName(name), key });
    }
    for (const name of Array.isArray(agent && agent.sx_skills) ? agent.sx_skills : []) {
      const key = skillKey(name);
      if (!key || seen.has(key) || directKeys.has(key)) continue;
      seen.add(key);
      inherited.push({ name, label: skillDisplayName(name), key });
    }
    return { direct, inherited, total: direct.length + inherited.length };
  }

  function agentJobs(agent) {
    if (!agent || !Array.isArray(agent.jobs)) return [];
    return agent.jobs.filter(job => job && compact(job.name, ''));
  }

  function renderAgentHeaderSubtitle(id) {
    const el = byID('selection-subtitle');
    if (!el) return;
    el.classList.remove('has-agent-assets');
    if (!id) {
      el.textContent = 'Recent work, active first.';
      return;
    }
    if (state.mode !== 'agent' || id === noAgentID) {
      el.textContent = groupDescription(id);
      return;
    }
    const agent = agentForSlug(id);
    const parts = ['<span class="selection-subtitle-text">' + esc(groupDescription(id)) + '</span>'];
    const teamLabel = agentTeamLabel(agent);
    if (teamLabel) parts.push('<span class="selection-subtitle-chip">' + esc(teamLabel) + '</span>');
    const groups = agentSkillGroups(agent);
    if (groups.total) {
      const label = groups.total + ' ' + (groups.total === 1 ? 'skill' : 'skills');
      parts.push('<button type="button" class="selection-subtitle-link" data-open-agent-skills="' + esc(id) + '">' + esc(label) + '</button>');
    }
    const jobs = agentJobs(agent);
    if (jobs.length) {
      const label = jobs.length + ' ' + (jobs.length === 1 ? 'job' : 'jobs');
      parts.push('<button type="button" class="selection-subtitle-link" data-open-agent-jobs="' + esc(id) + '">' + esc(label) + '</button>');
    }
    el.classList.add('has-agent-assets');
    el.innerHTML = parts.filter(Boolean).join('<span class="selection-subtitle-separator">·</span>');
    el.querySelector('[data-open-agent-skills]')?.addEventListener('click', e => {
      e.preventDefault();
      openAgentAssetsModal(e.currentTarget.dataset.openAgentSkills);
    });
    el.querySelector('[data-open-agent-jobs]')?.addEventListener('click', e => {
      e.preventDefault();
      openAgentAssetsModal(e.currentTarget.dataset.openAgentJobs);
    });
  }

  function openAgentAssetsModal(slug) {
    const agent = agentForSlug(slug);
    if (!agent) return;
    if (agentAssetsModalClose) agentAssetsModalClose();
    const groups = agentSkillGroups(agent);
    const overlay = document.createElement('div');
    overlay.id = 'agent-assets-modal-overlay';
    overlay.className = 'skills-modal-overlay agent-assets-overlay';
    overlay.innerHTML =
      '<div class="skills-modal agent-assets-modal" role="dialog" aria-modal="true" aria-labelledby="agent-assets-modal-title" tabindex="-1">'
      + '<div class="skills-modal-head">'
      + '<h2 class="skills-modal-title" id="agent-assets-modal-title">' + esc(agent.display_name || agent.slug || 'Agent files') + '</h2>'
      + '<button type="button" class="skills-modal-close" aria-label="Close">&times;</button>'
      + '</div>'
      + '<div class="agent-assets-body">'
      + agentFilesSectionHTML(agent)
      + agentJobSectionHTML(agent, agentJobs(agent))
      + agentSkillSectionHTML('Installed skills', groups.direct, 'No directly installed skills.', false)
      + agentSkillSectionHTML('Inherited skills from orgs and teams', groups.inherited, '', true)
      + '</div>'
      + '</div>';

    const close = mountLayeredDialog(
      overlay,
      '.skills-modal',
      () => overlay.remove(),
      () => {
        if (agentAssetsModalClose === close) agentAssetsModalClose = null;
      },
    );
    agentAssetsModalClose = close;
    overlay.querySelector('.skills-modal-close')?.addEventListener('click', close);
    overlay.addEventListener('click', e => {
      if (e.target === overlay) close();
    });
    overlay.addEventListener('click', e => {
      const agentDoc = e.target.closest('[data-open-agent-doc-inline]');
      if (agentDoc) {
        e.preventDefault();
        openAgentDocumentModal(agentDoc.dataset.openAgentDocInline, agentDoc.dataset.agentDisplayName || '');
        return;
      }
      const skillDoc = e.target.closest('[data-open-skill-doc-inline]');
      if (skillDoc) {
        e.preventDefault();
        openSkillDocumentModal(skillDoc.dataset.openSkillDocInline, '');
        return;
      }
      const toggle = e.target.closest('[data-agent-skill-list-toggle]');
      if (toggle) {
        e.preventDefault();
        toggleAgentSkillList(toggle);
      }
    });
  }

  function agentFilesSectionHTML(agent) {
    return '<section class="agent-assets-section">'
      + '<div class="agent-assets-label">Installed agent files</div>'
      + '<ul class="agent-assets-file-list">'
      + '<li><button type="button" class="agent-file-link" data-open-agent-doc-inline="' + esc(agent.slug || '') + '" data-agent-display-name="' + esc(agent.display_name || '') + '">'
      + agentFileIconHTML()
      + '<span>AGENTS.md</span>'
      + '</button></li>'
      + '</ul>'
      + '</section>';
  }

  function agentJobSectionHTML(agent, jobs) {
    let body = '<p class="agent-assets-hint">No scheduled jobs.</p>';
    if (jobs.length) {
      body = '<ul class="agent-job-summary-list">'
        + jobs.map(job => '<li class="agent-job-summary-item">'
          + '<div class="agent-job-summary-head">'
          + '<strong>' + esc(job.name || 'Untitled job') + '</strong>'
          + '<span class="agent-job-summary-pill' + (job.enabled ? ' is-enabled' : ' is-disabled') + '">' + esc(job.enabled ? 'Enabled' : 'Disabled') + '</span>'
          + '</div>'
          + agentJobSubmetaHTML(agent, job)
          + agentJobDefinitionHTML(job)
          + '<dl class="agent-job-summary-meta">'
          + agentJobMetaHTML('Schedule', agentJobScheduleText(job), agentJobTimezoneText(job))
          + agentJobMetaHTML('Next run', job.enabled ? compact(job.next_run_label, fullDate(job.next_run_at)) || 'Not scheduled' : 'Disabled')
          + '</dl>'
          + agentJobAdditionalReposHTML(job)
          + agentJobLastStatusHTML(job)
          + '</li>').join('')
        + '</ul>';
    }
    return '<section class="agent-assets-section">'
      + '<div class="agent-assets-label">Scheduled jobs</div>'
      + body
      + '</section>';
  }

  function agentJobSubmetaHTML(agent, job) {
    const label = compact(agent && agent.display_name, compact(agent && agent.slug, 'Agent'));
    const repo = compact(job && job.primary_repository, '');
    return '<dl class="agent-job-summary-submeta">'
      + '<div><dt>Agent</dt><dd>' + esc(label) + '</dd></div>'
      + (repo ? '<div><dt>Repo</dt><dd>' + esc(repo) + '</dd></div>' : '')
      + '</dl>';
  }

  function agentJobDefinitionHTML(job) {
    const definition = compact(job && job.definition, '');
    return definition ? '<p class="agent-job-summary-definition">' + esc(definition) + '</p>' : '';
  }

  function agentJobMetaHTML(label, value, detail) {
    value = compact(value, '-');
    detail = compact(detail, '');
    return '<div><dt>' + esc(label) + '</dt><dd><span>' + esc(value) + '</span>'
      + (detail ? '<small>' + esc(detail) + '</small>' : '')
      + '</dd></div>';
  }

  function agentJobScheduleText(job) {
    return compact(job && job.schedule_label, compact(job && job.cron_schedule, '')) || 'No schedule';
  }

  function agentJobTimezoneText(job) {
    return compact(job && job.timezone_label, compact(job && job.timezone, ''));
  }

  function agentJobLastStatusText(job) {
    const status = compact(job && job.last_execution_status, '');
    const error = compact(job && job.last_error, '');
    if (status) return humanizeSlug(status);
    if (error) return 'Error';
    return 'No runs yet';
  }

  function agentJobLastStatusDetail(job) {
    const status = compact(job && job.last_execution_status, '');
    const lastRun = compact(job && job.last_run_label, fullDate(job && job.last_run_at));
    const error = compact(job && job.last_error, '');
    const summary = compact(job && job.last_error_summary, '');
    if (error) return summary || truncateJobStatusDetail(error, 180);
    if (status && lastRun) return lastRun;
    return '';
  }

  function truncateJobStatusDetail(text, max) {
    if (!text || text.length <= max) return text;
    return text.slice(0, Math.max(0, max - 3)).trimEnd() + '...';
  }

  function agentJobLastStatusHTML(job) {
    const detail = agentJobLastStatusDetail(job);
    return '<div class="agent-job-summary-status">'
      + '<span>Last status</span>'
      + '<strong>' + esc(agentJobLastStatusText(job)) + '</strong>'
      + (detail ? '<small>' + esc(detail) + '</small>' : '')
      + '</div>';
  }

  function agentJobAdditionalReposHTML(job) {
    const repos = Array.isArray(job && job.additional_repositories)
      ? job.additional_repositories.map(repo => compact(repo, '')).filter(Boolean)
      : [];
    return repos.length
      ? '<p class="agent-job-summary-extra">Additional repositories: ' + esc(repos.join(', ')) + '</p>'
      : '';
  }

  function agentSkillSectionHTML(title, skills, emptyText, collapsible) {
    const count = skills.length;
    let body = emptyText ? '<p class="agent-assets-hint">' + esc(emptyText) + '</p>' : '';
    if (count) {
      const collapsed = collapsible && count > agentSkillPreviewCount;
      const listClass = 'skill-chip-list agent-assets-skill-list' + (collapsed ? ' is-collapsed' : '');
      body = '<div class="' + listClass + '" data-agent-skill-list>'
        + skills.map((skill, index) =>
          '<button type="button" class="skill-chip skill-chip-button" data-skill-index="' + index + '" data-open-skill-doc-inline="' + esc(skill.name) + '">' + esc(skill.label || skill.name) + '</button>'
        ).join('')
        + '</div>';
      if (collapsed) {
        body += '<button type="button" class="skill-chip-list-toggle" data-agent-skill-list-toggle data-expanded="false" data-count="' + esc(String(count)) + '">Show all (' + esc(String(count)) + ')</button>';
      }
    }
    return '<section class="agent-assets-section">'
      + '<div class="agent-assets-label">' + esc(title) + '</div>'
      + body
      + '</section>';
  }

  function agentFileIconHTML() {
    return '<svg class="agent-file-ico" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'
      + '<path d="M3.5 1.5h6L12.5 4.5v9A1 1 0 0 1 11.5 14.5h-8A1 1 0 0 1 2.5 13.5v-11A1 1 0 0 1 3.5 1.5z"/>'
      + '<path d="M9 1.5v3h3.5"/>'
      + '<path d="M5 8h6M5 10.5h6M5 5.5h2"/>'
      + '</svg>';
  }

  function toggleAgentSkillList(toggle) {
    const list = toggle.previousElementSibling;
    if (!list) return;
    const expanded = toggle.dataset.expanded === 'true';
    list.classList.toggle('is-collapsed', expanded);
    toggle.dataset.expanded = expanded ? 'false' : 'true';
    toggle.textContent = expanded ? 'Show all (' + (toggle.dataset.count || '') + ')' : 'Show less';
  }

  function openAgentDocumentModal(slug, displayName) {
    const title = (displayName || slug || 'Agent') + ' - AGENTS.md';
    const url = '/settings/org/agent-doc?slug=' + encodeURIComponent(slug || '');
    openDocumentModal({
      title,
      sidebar: false,
      load: async () => {
        const payload = await fetchDocumentJSON(url);
        return {
          title: (payload.display_name || displayName || slug || 'Agent') + ' - AGENTS.md',
          meta: payload.file_name || 'AGENTS.md',
          html: payload.content_html || '',
          raw: payload.content_md || '',
        };
      },
    });
  }

  function openSkillDocumentModal(name, path) {
    const params = new URLSearchParams({ name: name || '' });
    if (path) params.set('path', path);
    openDocumentModal({
      title: skillDisplayName(name),
      sidebar: true,
      skillName: name,
      load: async () => {
        const payload = await fetchDocumentJSON('/settings/org/skill-doc?' + params.toString());
        return skillPayloadForDocument(payload, name);
      },
    });
  }

  function skillPayloadForDocument(payload, fallbackName) {
    let meta = payload.path || '';
    if (payload.is_binary) meta += (meta ? ' - ' : '') + 'binary';
    if (payload.truncated) meta += (meta ? ' - ' : '') + 'truncated';
    return {
      title: payload.display_name || payload.name || skillDisplayName(fallbackName),
      meta,
      html: payload.content_html || '',
      raw: payload.is_binary ? 'Binary file - preview not available.' : (payload.content_md || ''),
      files: payload.files || [],
      activePath: payload.path || '',
      skillName: payload.name || fallbackName,
    };
  }

  async function fetchDocumentJSON(url) {
    const res = await fetch(url, { credentials: 'same-origin', headers: { Accept: 'application/json' } });
    if (!res.ok) throw new Error('request failed: ' + res.status);
    return await res.json();
  }

  function openDocumentModal(config) {
    if (agentDocumentModalClose) agentDocumentModalClose();
    const overlay = document.createElement('div');
    overlay.id = 'agent-document-modal-overlay';
    overlay.className = 'skills-modal-overlay agent-document-overlay';
    overlay.innerHTML =
      '<div class="agent-doc-modal" role="dialog" aria-modal="true" aria-labelledby="agent-document-title" tabindex="-1">'
      + '<div class="agent-doc-head">'
      + '<div><h2 id="agent-document-title">' + esc(config.title || 'Details') + '</h2><div class="agent-doc-meta" data-agent-doc-meta hidden></div></div>'
      + '<button type="button" class="agent-doc-close" aria-label="Close">&times;</button>'
      + '</div>'
      + '<div class="agent-doc-grid">'
      + '<main class="agent-doc-content">'
      + '<div class="agent-doc-loading" data-agent-doc-loading>Loading...</div>'
      + '<div class="agent-doc-error" data-agent-doc-error hidden></div>'
      + '<div class="markdown-body agent-doc-rendered" data-agent-doc-rendered hidden></div>'
      + '<pre class="agent-doc-raw" data-agent-doc-raw hidden></pre>'
      + '</main>'
      + '<aside class="agent-doc-sidebar" data-agent-doc-sidebar hidden><h3>Files</h3><ul class="agent-doc-file-list" data-agent-doc-file-list></ul></aside>'
      + '</div>'
      + '</div>';
    const close = mountLayeredDialog(
      overlay,
      '.agent-doc-modal',
      () => overlay.remove(),
      () => {
        if (agentDocumentModalClose === close) agentDocumentModalClose = null;
      },
    );
    agentDocumentModalClose = close;
    overlay.querySelector('.agent-doc-close')?.addEventListener('click', close);
    overlay.addEventListener('click', e => {
      if (e.target === overlay) close();
      const file = e.target.closest('[data-agent-doc-file]');
      if (file) {
        e.preventDefault();
        openSkillDocumentModal(file.dataset.skillName || '', file.dataset.agentDocFile || '');
      }
    });
    loadDocumentModalContent(overlay, config);
  }

  async function loadDocumentModalContent(overlay, config) {
    const title = overlay.querySelector('#agent-document-title');
    const meta = overlay.querySelector('[data-agent-doc-meta]');
    const loading = overlay.querySelector('[data-agent-doc-loading]');
    const errorEl = overlay.querySelector('[data-agent-doc-error]');
    const rendered = overlay.querySelector('[data-agent-doc-rendered]');
    const raw = overlay.querySelector('[data-agent-doc-raw]');
    const sidebar = overlay.querySelector('[data-agent-doc-sidebar]');
    const fileList = overlay.querySelector('[data-agent-doc-file-list]');
    try {
      const doc = await config.load();
      title.textContent = doc.title || config.title || 'Details';
      meta.textContent = doc.meta || '';
      meta.hidden = !doc.meta;
      loading.hidden = true;
      if (doc.html) {
        rendered.innerHTML = DOMPurify.sanitize(doc.html);
        rendered.hidden = false;
        raw.hidden = true;
      } else {
        raw.textContent = doc.raw || '';
        raw.hidden = false;
        rendered.hidden = true;
      }
      if (Array.isArray(doc.files) && doc.files.length) {
        sidebar.hidden = false;
        fileList.innerHTML = doc.files.map(file =>
          '<li><button type="button" class="agent-doc-file-item' + (file.path === doc.activePath ? ' is-active' : '') + '" data-skill-name="' + esc(doc.skillName || config.skillName || '') + '" data-agent-doc-file="' + esc(file.path || '') + '">' + esc(file.path || '') + '</button></li>'
        ).join('');
      }
    } catch (err) {
      console.warn('agent-doc: failed to load document', err);
      loading.hidden = true;
      errorEl.hidden = false;
      errorEl.textContent = 'Failed to load details.';
    }
  }

  function mountLayeredDialog(overlay, dialogSelector, removeFn, afterClose) {
    const previousFocus = document.activeElement;
    const dialog = overlay.querySelector(dialogSelector);
    const focusableSelector = [
      'a[href]', 'button:not([disabled])', 'input:not([disabled])',
      'select:not([disabled])', 'textarea:not([disabled])',
      '[tabindex]:not([tabindex="-1"])',
    ].join(',');
    const close = () => {
      document.removeEventListener('keydown', onKey);
      removeFn();
      if (typeof afterClose === 'function') afterClose();
      if (previousFocus && typeof previousFocus.focus === 'function' && document.contains(previousFocus)) {
        previousFocus.focus();
      }
    };
    const onKey = e => {
      if (topAgentOverlay() !== overlay) return;
      if (e.key === 'Escape') {
        e.preventDefault();
        close();
        return;
      }
      if (e.key !== 'Tab') return;
      const items = Array.from(dialog.querySelectorAll(focusableSelector));
      if (!items.length) return;
      const first = items[0];
      const last = items[items.length - 1];
      const active = document.activeElement;
      if (e.shiftKey && (active === first || !dialog.contains(active))) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && (active === last || !dialog.contains(active))) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKey);
    document.body.appendChild(overlay);
    requestAnimationFrame(() => {
      const first = overlay.querySelector(focusableSelector) || dialog;
      if (first && typeof first.focus === 'function') first.focus();
    });
    return close;
  }

  function topAgentOverlay() {
    const overlays = Array.from(document.querySelectorAll('.agent-assets-overlay, .agent-document-overlay'));
    return overlays[overlays.length - 1] || null;
  }
