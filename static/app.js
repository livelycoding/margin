'use strict';

(() => {
  const $ = (id) => document.getElementById(id);
  const audio = $('audio');
  // Evenly spaced slider stops retain fine control below 2× and reach 5×.
  const playbackRates = [0.75, 1, 1.25, 1.5, 1.75, 2, 2.5, 3, 3.5, 4, 4.5, 5];
  let previewAudio = $('voice-preview-audio');
  const previewState = { request: 0, status: 'idle', message: '', voiceId: null };
  const storage = {
    get(key, fallback = null) { try { return localStorage.getItem(`margin:${key}`) ?? fallback; } catch { return fallback; } },
    set(key, value) { try { localStorage.setItem(`margin:${key}`, String(value)); } catch { /* Private browsing may disable persistence. */ } },
    remove(key) { try { localStorage.removeItem(`margin:${key}`); } catch { /* No persistence available. */ } },
  };
  const state = { config: null, episodes: [], selected: null, selectedId: null, previousStatuses: new Map(), loaded: false, polling: false, creating: false, importing: false, sourceUrl: '', audioEpisodeId: null, cuesSignature: '', cueElements: new Map(), currentCue: -1, selectionRequest: 0, deleteConfirm: false, lastSaved: 0, rename: null, titleRevision: 0, savedTitles: new Map(), libraryRevision: 0 };
  const voiceBrowser = { accent: 'all', gender: 'all', favoritesOnly: false, cards: new Map(), favorites: new Set(), recast: null };
  let restoreSource = null;
  try {
    const favorites = JSON.parse(storage.get('favorite-voices', '[]'));
    if (Array.isArray(favorites)) voiceBrowser.favorites = new Set(favorites.filter((id) => typeof id === 'string'));
  } catch { /* Ignore an old or malformed preference. */ }
  const statusLabels = { queued: 'In the queue', generating: 'Making audio', complete: 'Ready to listen', failed: 'Needs attention', cancelled: 'Cancelled' };
  // Editorial guides to the character of the cached natural-pace samples.
  const voiceTags = {
    af_heart: ['Bright', 'Smooth', 'Lively'],
    af_bella: ['Bright', 'Smooth', 'Steady'],
    af_nicole: ['Soft', 'Breathy', 'Unhurried'],
    af_sarah: ['Bright', 'Airy', 'Steady'],
    af_alloy: ['Low', 'Rounded', 'Measured'],
    af_aoede: ['Warm', 'Smooth', 'Brisk'],
    af_jessica: ['Light', 'Crisp', 'Brisk'],
    af_kore: ['Low', 'Clear', 'Steady'],
    af_nova: ['Warm', 'Rounded', 'Steady'],
    af_river: ['Bright', 'Airy', 'Brisk'],
    af_sky: ['Warm', 'Soft', 'Steady'],
    am_michael: ['Deep', 'Clear', 'Measured'],
    am_adam: ['Low', 'Grainy', 'Brisk'],
    am_echo: ['Deep', 'Rounded', 'Steady'],
    am_eric: ['Warm', 'Smooth', 'Brisk'],
    am_fenrir: ['Low', 'Crisp', 'Steady'],
    am_liam: ['Low', 'Textured', 'Brisk'],
    am_onyx: ['Deep', 'Dark', 'Measured'],
    am_puck: ['Deep', 'Clear', 'Steady'],
    am_santa: ['Textured', 'Raspy', 'Measured'],
    bf_emma: ['Bright', 'Crisp', 'Brisk'],
    bf_alice: ['Light', 'Airy', 'Measured'],
    bf_isabella: ['Light', 'Clear', 'Brisk'],
    bf_lily: ['Warm', 'Smooth', 'Steady'],
    bm_george: ['Warm', 'Clear', 'Measured'],
    bm_daniel: ['Low', 'Textured', 'Brisk'],
    bm_fable: ['Low', 'Rounded', 'Unhurried'],
    bm_lewis: ['Deep', 'Grainy', 'Measured'],
  };
  const isPending = (episode) => episode && ['queued', 'generating'].includes(episode.status);
  const apiPath = (id, suffix = '') => `/api/episodes/${encodeURIComponent(id)}${suffix}`;
  const clamp = (number, low, high) => Math.max(low, Math.min(high, Number(number) || 0));
  const progressOf = (episode) => Math.round(clamp(episode.progress, 0, 100));
  const remainingTime = (episode, compact = false) => {
    if (episode.status !== 'generating' || episode.cancel_requested) return '';
    if (episode.generation_phase === 'finalizing') return compact ? 'Finishing…' : 'Finishing audio…';
    const seconds = episode.eta_seconds;
    if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds < 0) {
      return compact ? 'Estimating…' : 'Estimating time remaining…';
    }
    // Coarse estimates stay readable as sentence timings vary; polling supplies updates.
    const duration = seconds < 60
      ? `${Math.max(10, Math.ceil(seconds / 10) * 10)} sec`
      : `${Math.ceil(seconds / 60)} min`;
    return compact ? `~${duration} left` : `About ${duration} remaining`;
  };
  const formatTime = (seconds) => {
    const n = Math.max(0, Math.floor(Number(seconds) || 0));
    const s = String(n % 60).padStart(2, '0');
    const m = Math.floor(n / 60);
    return m >= 60 ? `${Math.floor(m / 60)}:${String(m % 60).padStart(2, '0')}:${s}` : `${m}:${s}`;
  };
  const words = (text) => (text.trim().match(/\S+/g) || []).length;
  const element = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  };
  const playbackIcon = (kind = 'play') => {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('class', 'playback-icon');
    svg.setAttribute('aria-hidden', 'true');
    svg.setAttribute('focusable', 'false');
    const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
    path.setAttribute('d', kind === 'pause' ? 'M7 5h4v14H7zM13 5h4v14h-4z'
      : kind === 'stop' ? 'M6 6h12v12H6z' : 'M8 5l12 7-12 7z');
    svg.append(path);
    return svg;
  };
  const safeSourceUrl = (value) => {
    try { const url = new URL(value); return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password ? url.href : null; } catch { return null; }
  };

  async function api(path, options = {}) {
    const headers = { Accept: 'application/json', ...options.headers };
    if (options.body !== undefined) headers['Content-Type'] = 'application/json';
    if (options.method && options.method !== 'GET') headers['X-CSRF-Token'] = state.config?.csrf_token || '';
    let response;
    try { response = await fetch(path, { ...options, headers, credentials: 'same-origin' }); }
    catch { throw new Error('The local server is not reachable. Make sure Margin is running, then try again.'); }
    const data = await response.json().catch(() => ({}));
    if (!response.ok) {
      const message = typeof data.error === 'string' ? data.error : typeof data.detail === 'string' ? data.detail : typeof data.message === 'string' ? data.message : `The request could not be completed (${response.status}).`;
      throw new Error(message);
    }
    return data;
  }

  function toast(message, options = {}) {
    const box = element('div', `toast${options.error ? ' error' : ''}`);
    box.append(element('p', '', message));
    if (options.episodeId) {
      const listen = element('button', '', 'Listen');
      listen.type = 'button';
      listen.addEventListener('click', () => { selectEpisode(options.episodeId); box.remove(); });
      box.append(listen);
    }
    const close = element('button', 'dismiss-toast', '×');
    close.type = 'button'; close.setAttribute('aria-label', 'Dismiss notification');
    close.addEventListener('click', () => box.remove()); box.append(close);
    $('toast-container').append(box);
    setTimeout(() => box.remove(), options.error ? 12000 : 9000);
  }

  function formError(message = '') { $('form-error').textContent = message; $('form-error').hidden = !message; }
  function updateForm() {
    const count = words($('article-text').value);
    $('word-count').textContent = `${count.toLocaleString()} ${count === 1 ? 'word' : 'words'}`;
    $('reading-time').textContent = count ? `About ${Math.max(1, Math.round(count / (155 * Number($('pace-select').value))))} min of listening` : 'Your next listen starts here';
    $('generate-button').disabled = !state.config?.engine?.ready || !count || state.creating || state.importing;
    $('generate-button').firstElementChild.textContent = state.creating ? 'Adding to your listening shelf…' : 'Create my audio';
    $('edit-episode-button').disabled = state.creating || state.importing;
  }

  function selectedPreviewVoice() {
    return state.config?.voices?.find((voice) => voice.id === $('voice-select').value);
  }

  function voiceDetails(voice) {
    return `${voice.accent} · ${voice.gender === 'male' ? 'Male' : 'Female'}`;
  }

  function normalizeVoice(voice) {
    const shortName = voice.short_name || (voice.name || voice.id).split(' · ')[0];
    return {
      ...voice, short_name: shortName, tags: voiceTags[voice.id] || [],
      accent: voice.accent || (voice.id.startsWith('b') ? 'British' : 'American'),
      gender: voice.gender || (voice.id[1] === 'm' ? 'male' : 'female'),
    };
  }

  function renderVoiceSelection() {
    const selected = selectedPreviewVoice();
    $('voice-picker-button').disabled = !selected;
    $('selected-voice-name').textContent = selected?.short_name || 'No voices available';
    $('selected-voice-details').textContent = selected ? voiceDetails(selected) : '';
    $('voice-picker-button').setAttribute('aria-label', selected ? `Browse voices. Selected: ${selected.short_name}, ${voiceDetails(selected)}` : 'Browse voices');
    const recast = voiceBrowser.recast;
    const picked = recast ? state.config?.voices?.find((voice) => voice.id === recast.voice) : selected;
    $('voice-picker-selected').textContent = picked ? `${picked.short_name} · ${voiceDetails(picked)}` : 'Choose a different voice';
    for (const [id, card] of voiceBrowser.cards) {
      const isSelected = id === picked?.id;
      const isOriginal = id === recast?.source.voice;
      card.root.classList.toggle('is-selected', isSelected);
      card.choice.setAttribute('aria-pressed', String(isSelected));
      card.choice.setAttribute('aria-label', `Choose ${card.voice.short_name}, ${voiceDetails(card.voice)}${isOriginal ? ', current voice' : ''}`);
      card.choice.disabled = !!recast?.saving;
      card.selected.textContent = isOriginal ? 'Current voice' : 'Selected';
      card.selected.hidden = !isSelected && !isOriginal;
    }
    $('voice-picker-done').disabled = !!recast && (recast.saving || !picked || picked.id === recast.source.voice);
    $('voice-picker-done').textContent = recast ? recast.saving ? 'Creating version…' : 'Create new version' : 'Done';
    $('voice-picker-cancel').disabled = !!recast?.saving;
    $('voice-picker-close').disabled = !!recast?.saving;
    renderVoicePreview();
  }

  function chooseVoice(id) {
    if (voiceBrowser.recast?.saving || !state.config?.voices?.some((voice) => voice.id === id)) return;
    stopVoicePreview();
    if (voiceBrowser.recast) {
      voiceBrowser.recast.voice = id;
      recastError();
    } else {
      $('voice-select').value = id;
      storage.set('voice', id);
    }
    renderVoiceSelection();
    const name = state.config.voices.find((voice) => voice.id === id).short_name;
    $('voice-picker-status').textContent = voiceBrowser.recast
      ? id === voiceBrowser.recast.source.voice ? 'Choose a different voice to create a new version.' : `${name} selected for the new version.`
      : `${name} selected for your next listen.`;
  }

  function renderVoicePreview() {
    const voice = selectedPreviewVoice();
    const available = !!voice?.preview_url;
    const isCurrent = previewState.voiceId === voice?.id;
    const active = isCurrent && ['loading', 'playing'].includes(previewState.status);
    const failed = isCurrent && previewState.status === 'error';
    const name = voice?.name || voice?.id || 'selected voice';
    const action = active ? 'Stop preview' : failed ? 'Retry preview' : 'Preview voice';
    $('voice-preview-button').disabled = !available;
    $('voice-preview-button').classList.toggle('active', active);
    $('voice-preview-button').setAttribute('aria-label', `${action}: ${name}`);
    $('voice-preview-icon').replaceChildren(playbackIcon(active ? 'stop' : 'play'));
    $('voice-preview-label').textContent = action;
    $('voice-preview-status').classList.toggle('preview-error', failed);
    $('voice-preview-status').textContent = (isCurrent && previewState.message) || (available ? 'Short sample · natural pace' : state.config ? 'Sample unavailable for this voice.' : 'Loading voice samples…');
    for (const [id, card] of voiceBrowser.cards) {
      const current = previewState.voiceId === id;
      const playing = current && ['loading', 'playing'].includes(previewState.status);
      const error = current && previewState.status === 'error';
      const label = playing ? 'Stop' : error ? 'Retry' : 'Preview';
      card.preview.classList.toggle('active', playing);
      card.preview.setAttribute('aria-label', `${label} sample: ${card.voice.short_name}`);
      card.preview.replaceChildren(playbackIcon(playing ? 'stop' : 'play'), element('span', '', label));
      card.status.classList.toggle('preview-error', error);
      card.status.textContent = current && previewState.status === 'loading' ? 'Loading sample…'
        : current && previewState.status === 'playing' ? 'Playing sample'
        : error ? 'Could not play. Try again.' : !card.voice.preview_url ? 'Sample unavailable' : 'Natural pace';
    }
    if ($('voice-picker').open) $('voice-picker-status').textContent = previewState.message;
  }

  function stopVoicePreview(message = '', voiceId = previewState.voiceId) {
    // Invalidate pending play promises before aborting their media requests.
    previewState.request++;
    previewState.status = 'idle'; previewState.message = message;
    previewState.voiceId = message ? voiceId : null;
    previewAudio.pause(); previewAudio.removeAttribute('src'); previewAudio.load();
    renderVoicePreview();
  }

  function toggleVoicePreview(voiceId = $('voice-select').value) {
    if (voiceId === previewState.voiceId && ['loading', 'playing'].includes(previewState.status)) { stopVoicePreview('Preview stopped.'); return; }
    const voice = state.config?.voices?.find((item) => item.id === voiceId);
    if (!voice?.preview_url) { renderVoicePreview(); return; }
    stopVoicePreview();
    const request = ++previewState.request;
    // A fresh element keeps queued media events from a previous voice isolated.
    const sample = document.createElement('audio');
    sample.id = 'voice-preview-audio'; sample.preload = 'none'; sample.hidden = true;
    previewAudio.replaceWith(sample); previewAudio = sample;
    const current = () => request === previewState.request && sample === previewAudio;
    const name = voice.short_name || voice.name || voice.id;
    const fail = () => {
      if (!current()) return;
      stopVoicePreview();
      previewState.voiceId = voice.id;
      previewState.status = 'error';
      previewState.message = `Could not play ${name}'s sample. Try again.`;
      renderVoicePreview();
    };
    sample.addEventListener('playing', () => {
      if (!current()) { sample.pause(); return; }
      previewState.status = 'playing'; previewState.message = `Playing ${name} · natural pace`;
      renderVoicePreview();
    });
    sample.addEventListener('waiting', () => {
      if (!current()) return;
      previewState.status = 'loading'; previewState.message = `Loading ${name}…`;
      renderVoicePreview();
    });
    sample.addEventListener('ended', () => { if (current()) stopVoicePreview(`${name}'s sample finished · natural pace`); });
    sample.addEventListener('pause', () => {
      if (current() && !sample.ended && previewState.status === 'playing') stopVoicePreview('Preview stopped.');
    });
    sample.addEventListener('error', fail);
    previewState.voiceId = voice.id;
    previewState.status = 'loading'; previewState.message = `Loading ${name}…`;
    renderVoicePreview();
    audio.pause();
    sample.src = voice.preview_url;
    sample.playbackRate = 1;
    sample.play().catch(fail);
  }

  function updateVoiceFavorite(card) {
    const favorite = voiceBrowser.favorites.has(card.voice.id);
    card.favorite.setAttribute('aria-pressed', String(favorite));
    card.favorite.setAttribute('aria-label', `${favorite ? 'Remove' : 'Add'} ${card.voice.short_name} ${favorite ? 'from' : 'to'} favorites`);
    card.favorite.title = favorite ? 'Remove from favorites' : 'Add to favorites';
  }

  function buildVoiceCards() {
    $('voice-grid').replaceChildren();
    voiceBrowser.cards.clear();
    for (const voice of state.config?.voices || []) {
      const root = element('article', 'voice-card');
      root.dataset.accent = voice.accent;
      const choice = element('button', 'voice-card-choice');
      choice.type = 'button';
      choice.setAttribute('aria-label', `Choose ${voice.short_name}, ${voiceDetails(voice)}`);
      choice.addEventListener('click', () => chooseVoice(voice.id));
      const initial = element('span', 'voice-card-initial', voice.short_name[0]);
      initial.setAttribute('aria-hidden', 'true');
      const copy = element('span', 'voice-card-copy');
      const selected = element('span', 'voice-card-selected', 'Selected');
      selected.hidden = true;
      copy.append(element('span', 'voice-card-name', voice.short_name), element('span', 'voice-card-meta', voiceDetails(voice)), selected);
      choice.append(initial, copy);
      const favorite = element('button', 'voice-card-favorite');
      favorite.type = 'button';
      const star = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
      star.setAttribute('viewBox', '0 0 24 24'); star.setAttribute('aria-hidden', 'true'); star.setAttribute('focusable', 'false');
      const starPath = document.createElementNS('http://www.w3.org/2000/svg', 'path');
      starPath.setAttribute('d', 'm12 3 2.8 5.7 6.3.9-4.6 4.5 1.1 6.3-5.6-3-5.6 3 1.1-6.3L2.9 9.6l6.3-.9Z');
      star.append(starPath); favorite.append(star);
      const previewRow = element('div', 'voice-card-preview-row');
      const preview = element('button', 'voice-card-preview');
      preview.type = 'button'; preview.disabled = !voice.preview_url;
      preview.addEventListener('click', () => toggleVoicePreview(voice.id));
      const status = element('span', 'voice-card-status');
      status.id = `voice-status-${voice.id}`;
      preview.setAttribute('aria-describedby', status.id);
      previewRow.append(preview, status);
      const tags = element('div', 'voice-card-tags');
      tags.id = `voice-tags-${voice.id}`;
      for (const tag of voice.tags) tags.append(element('span', 'voice-tag', tag), document.createTextNode(' '));
      choice.setAttribute('aria-describedby', tags.id);
      root.append(choice, favorite, tags, previewRow);
      const card = { root, choice, selected, favorite, preview, status, voice };
      voiceBrowser.cards.set(voice.id, card);
      updateVoiceFavorite(card);
      favorite.addEventListener('click', () => {
        if (voiceBrowser.favorites.has(voice.id)) voiceBrowser.favorites.delete(voice.id);
        else voiceBrowser.favorites.add(voice.id);
        storage.set('favorite-voices', JSON.stringify([...voiceBrowser.favorites]));
        updateVoiceFavorite(card);
        filterVoiceCards();
        // Removing a favorite can hide the focused card; keep keyboard focus in the browser.
        if (root.hidden) $('voice-favorites-filter').focus();
      });
      $('voice-grid').append(root);
    }
    filterVoiceCards();
    renderVoiceSelection();
  }

  function filterVoiceCards() {
    const query = $('voice-search').value.trim().toLocaleLowerCase();
    const terms = query.split(/\s+/).filter(Boolean);
    let count = 0;
    for (const card of voiceBrowser.cards.values()) {
      const voice = card.voice;
      const haystack = `${voice.short_name} ${voice.accent} ${voice.gender} ${voice.tags.join(' ')}`.toLocaleLowerCase();
      const visible = (voiceBrowser.accent === 'all' || voice.accent === voiceBrowser.accent)
        && (voiceBrowser.gender === 'all' || voice.gender === voiceBrowser.gender)
        && (!voiceBrowser.favoritesOnly || voiceBrowser.favorites.has(voice.id))
        && terms.every((term) => ['male', 'female'].includes(term) ? voice.gender === term : haystack.includes(term));
      card.root.hidden = !visible;
      if (visible) count++;
    }
    // A hidden preview would have no visible stop button after filtering.
    if (voiceBrowser.cards.get(previewState.voiceId)?.root.hidden && ['loading', 'playing'].includes(previewState.status)) stopVoicePreview();
    $('voice-result-count').textContent = `${count} ${count === 1 ? 'voice' : 'voices'}${count !== voiceBrowser.cards.size ? ` of ${voiceBrowser.cards.size}` : ' to explore'}`;
    $('voice-empty').hidden = count > 0;
    $('voice-empty-message').textContent = voiceBrowser.favoritesOnly && !voiceBrowser.favorites.size
      ? 'Star the voices you like to find them here.' : 'No voices match these filters.';
    for (const button of document.querySelectorAll('[data-voice-accent]')) button.setAttribute('aria-pressed', String(button.dataset.voiceAccent === voiceBrowser.accent));
    for (const button of document.querySelectorAll('[data-voice-gender]')) button.setAttribute('aria-pressed', String(button.dataset.voiceGender === voiceBrowser.gender));
    $('voice-favorites-filter').setAttribute('aria-pressed', String(voiceBrowser.favoritesOnly));
    $('voice-favorites-filter').firstElementChild.textContent = voiceBrowser.favoritesOnly ? '★' : '☆';
  }

  function openVoicePicker(recast = null) {
    if ($('voice-picker').open) return;
    stopVoicePreview();
    voiceBrowser.recast = recast;
    $('voice-picker').classList.toggle('is-recasting', !!recast);
    $('voice-picker-title').textContent = recast ? 'Recast this listen' : 'A voice for every listen.';
    $('voice-picker-description').textContent = recast
      ? 'Choose a different narrator. Your original stays on the shelf, alongside a new recording and transcript.'
      : 'Listen to a sample, choose a voice, and save your favorites.';
    $('voice-footer-label').textContent = recast ? 'NEW NARRATOR' : 'YOUR NARRATOR';
    $('recast-context').hidden = !recast;
    $('voice-picker-cancel').hidden = !recast;
    recastError();
    if (recast) {
      $('recast-title').textContent = recast.source.title || 'Untitled listen';
      $('recast-title').title = recast.source.title || 'Untitled listen';
      const voice = state.config.voices.find((item) => item.id === recast.source.voice);
      const pace = Array.from($('pace-select').options).find((option) => Number(option.value) === Number(recast.source.speed));
      $('recast-details').textContent = `Current voice: ${voice?.short_name || recast.source.voice} · Reading pace: ${pace?.textContent || `${recast.source.speed}×`}`;
    }
    filterVoiceCards();
    renderVoiceSelection();
    document.body.classList.add('voice-picker-open');
    $('voice-picker').showModal();
    $('voice-search').focus();
  }

  function closeVoicePicker() {
    if (voiceBrowser.recast?.saving) return;
    $('voice-picker').close();
  }

  function beginRecast() {
    const episode = state.selected;
    if (!state.config?.features?.recast || episode?.status !== 'complete') return;
    const source = Object.freeze({ id: episode.id, title: episode.title, voice: episode.voice, speed: episode.speed });
    openVoicePicker({ source, voice: source.voice, saving: false });
  }

  function recastError(message = '') {
    $('recast-error').textContent = message;
    $('recast-error').hidden = !message;
  }

  async function submitRecast() {
    const recast = voiceBrowser.recast;
    if (!recast || recast.saving || !recast.voice || recast.voice === recast.source.voice) return;
    recast.saving = true;
    stopVoicePreview(); recastError(); renderVoiceSelection();
    try {
      const result = await api(apiPath(recast.source.id, '/recast'), { method: 'POST', body: JSON.stringify({ voice: recast.voice }) });
      const episode = result.episode || result;
      state.libraryRevision++;
      state.previousStatuses.set(episode.id, episode.status);
      state.episodes = [episode, ...state.episodes.filter((item) => item.id !== episode.id)];
      recast.saving = false;
      closeVoicePicker();
      renderLibrary(); await selectEpisode(episode.id, episode);
      toast('New version added to your shelf. Audio is being made in the background.');
      if (window.matchMedia('(max-width: 760px)').matches) $('listen-heading').scrollIntoView({ behavior: 'smooth', block: 'start' });
    } catch (error) {
      recast.saving = false;
      recastError(error.message);
      renderVoiceSelection();
    }
  }

  function setTab(mode, focus = false) {
    const urlMode = mode === 'url';
    if (!urlMode) { state.sourceUrl = ''; $('import-note').hidden = true; }
    for (const tab of [$('text-tab'), $('url-tab')]) {
      const active = (tab.id === 'url-tab') === urlMode;
      tab.classList.toggle('active', active); tab.setAttribute('aria-selected', String(active)); tab.tabIndex = active ? 0 : -1;
      if (active && focus) tab.focus();
    }
    $('url-source').hidden = !urlMode;
    // The editor stays available after importing so the article can always be reviewed.
    $('text-source').setAttribute('role', urlMode ? 'group' : 'tabpanel');
  }

  async function importArticle() {
    const url = safeSourceUrl($('source-url').value.trim());
    if (!url) { formError('Enter a complete http or https article link.'); $('source-url').focus(); return; }
    state.importing = true; $('import-button').disabled = true; $('import-button').textContent = 'Importing…'; formError(); updateForm();
    try {
      const article = await api('/api/import', { method: 'POST', body: JSON.stringify({ url }) });
      $('episode-title').value = article.title || '';
      $('article-text').value = article.text || '';
      state.sourceUrl = safeSourceUrl(article.url || url) || url;
      $('import-note').textContent = 'Article imported. You can edit the title and text before making audio.';
      $('import-note').hidden = false;
      $('article-text').focus();
    } catch (error) { formError(error.message); }
    finally { state.importing = false; $('import-button').disabled = false; $('import-button').textContent = 'Import'; updateForm(); }
  }

  function restoreEpisodeToEditor(episode = state.selected, replaceDraft = false) {
    if (state.creating || state.importing || !['failed', 'cancelled'].includes(episode?.status)
      || typeof episode.text !== 'string' || !episode.text.trim()) return;

    const title = episode.title || '';
    const sourceUrl = safeSourceUrl(episode.source_url) || '';
    const currentTitle = $('episode-title').value.trim();
    const currentText = $('article-text').value.trim();
    const currentUrl = !$('url-source').hidden ? $('source-url').value.trim() : state.sourceUrl;
    const replacesDraft = (currentTitle && currentTitle !== title.trim())
      || (currentText && currentText !== episode.text.trim()) || (currentUrl && currentUrl !== sourceUrl);
    if (replacesDraft && !replaceDraft) {
      restoreSource = episode;
      $('restore-title').textContent = title || 'Untitled listen';
      $('restore-dialog').returnValue = 'cancel';
      $('restore-dialog').showModal();
      return;
    }

    // The saved listen stays intact; submitting the edited text creates a new one.
    setTab('text');
    $('episode-title').value = title;
    $('article-text').value = episode.text;
    state.sourceUrl = sourceUrl;
    $('source-url').value = sourceUrl;
    const hasVoice = state.config?.voices?.some((voice) => voice.id === episode.voice);
    if (hasVoice) chooseVoice(episode.voice); else stopVoicePreview();
    const speed = Number(episode.speed);
    if (Number.isFinite(speed) && speed >= 0.7 && speed <= 1.3) {
      const pace = $('pace-select');
      pace.querySelector('[data-restored-pace]')?.remove();
      // API-created listens can have a pace between the standard menu choices.
      if (!Array.from(pace.options).some((option) => Number(option.value) === speed)) {
        const option = element('option', '', `Saved pace · ${speed}×`);
        option.value = String(speed); option.dataset.restoredPace = '';
        pace.append(option);
      }
      pace.value = String(speed);
    }
    $('import-note').textContent = 'Saved text restored. Edit it, then choose “Create my audio” to make a new listen.'
      + (hasVoice ? '' : ' The original voice is unavailable; choose a voice before creating audio.');
    $('import-note').hidden = false;
    formError(); updateForm();
    $('article-text').focus({ preventScroll: true });
    $('article-text').setSelectionRange(0, 0);
    $('article-text').scrollTop = 0;
    $('compose-heading').scrollIntoView({ behavior: 'smooth', block: 'start' });
  }

  async function createEpisode(event) {
    event.preventDefault();
    const text = $('article-text').value.trim();
    if (!text || state.creating) return;
    if (text.length > 150000) { formError('Please use up to 150,000 characters per listen.'); return; }
    state.creating = true; formError(); updateForm();
    try {
      const payload = { title: $('episode-title').value.trim(), text, voice: $('voice-select').value, speed: Number($('pace-select').value) };
      if (state.sourceUrl) payload.source_url = state.sourceUrl;
      const result = await api('/api/episodes', { method: 'POST', body: JSON.stringify(payload) });
      const episode = result.episode || result;
      state.libraryRevision++;
      state.previousStatuses.set(episode.id, episode.status);
      state.episodes = [episode, ...state.episodes.filter((item) => item.id !== episode.id)];
      renderLibrary(); await selectEpisode(episode.id, episode);
      toast('Added to your shelf. Audio is being made in the background.');
      if (window.matchMedia('(max-width: 760px)').matches) $('listen-heading').scrollIntoView({ behavior: 'smooth', block: 'start' });
    } catch (error) { formError(error.message); }
    finally { state.creating = false; updateForm(); }
  }

  function renderLibrary() {
    const library = $('library'); library.replaceChildren();
    $('episode-count').textContent = `${state.episodes.length} saved`;
    if (!state.episodes.length) { library.append(element('div', 'library-empty', 'Your listening shelf is waiting.')); return; }
    for (const episode of state.episodes) {
      const button = element('button', `library-item${episode.id === state.selectedId ? ' selected' : ''}`);
      button.type = 'button'; button.setAttribute('aria-pressed', String(episode.id === state.selectedId));
      const icon = element('span', 'library-icon');
      if (episode.status === 'complete') icon.append(playbackIcon());
      else icon.textContent = episode.status === 'failed' ? '!' : episode.status === 'cancelled' ? '−' : '◌';
      icon.setAttribute('aria-hidden', 'true');
      const info = element('span'); info.append(element('span', 'library-title', episode.title || 'Untitled listen'));
      const voice = episode.voice_name?.split(' · ')[0] || state.config?.voices?.find((item) => item.id === episode.voice)?.short_name || episode.voice || 'Local voice';
      const parts = [voice];
      if (episode.word_count) parts.push(`${Number(episode.word_count).toLocaleString()} words`);
      if (episode.status === 'generating') { const estimate = remainingTime(episode, true); if (estimate) parts.push(estimate); }
      else if (episode.duration) parts.push(formatTime(episode.duration));
      else { const date = new Date(episode.created_at); if (!Number.isNaN(date.getTime())) parts.push(date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })); }
      info.append(element('span', 'library-meta', parts.join(' · ')));
      const status = episode.status === 'generating' ? `${progressOf(episode)}%` : episode.status === 'complete' ? 'Ready' : episode.status === 'queued' ? 'Queued' : episode.status === 'cancelled' ? 'Cancelled' : 'Failed';
      button.append(icon, info, element('span', `library-status ${episode.status}`, status));
      button.addEventListener('click', () => selectEpisode(episode.id)); library.append(button);
    }
  }

  function savePosition(force = false) {
    if (!state.audioEpisodeId || !Number.isFinite(audio.currentTime)) return;
    const now = Date.now();
    if (force || now - state.lastSaved > 3000) { storage.set(`position:${state.audioEpisodeId}`, audio.ended ? 0 : audio.currentTime); state.lastSaved = now; }
  }

  function resetPlayer() {
    closeRename();
    savePosition(true); audio.pause(); audio.removeAttribute('src'); audio.load();
    state.audioEpisodeId = null; state.cuesSignature = ''; state.currentCue = -1; state.cueElements.clear();
    $('transcript-search').value = ''; $('audio-seek').value = '0'; $('current-time').textContent = '0:00';
    resetDeleteConfirmation();
  }

  async function selectEpisode(id, initial = null) {
    if (state.selectedId === id && state.selected && !initial) return;
    const request = ++state.selectionRequest;
    if (id !== state.selectedId) resetPlayer();
    state.selectedId = id; storage.set('selected', id); state.selected = initial;
    renderLibrary();
    if (initial) renderSelected();
    else { $('player-empty').hidden = true; $('episode-player').hidden = true; }
    try {
      const titleRevision = state.titleRevision;
      const result = initial && Array.isArray(initial.cues) ? initial : await api(apiPath(id));
      if (request !== state.selectionRequest || id !== state.selectedId) return;
      state.selected = preserveSavedTitle(result.episode || result, titleRevision); renderSelected();
    } catch (error) {
      if (request !== state.selectionRequest) return;
      toast(error.message, { error: true });
      if (!state.selected) { $('player-empty').hidden = false; }
    }
  }

  function renderSelected() {
    const episode = state.selected;
    $('player-empty').hidden = !!episode; $('episode-player').hidden = !episode;
    if (!episode) return;
    $('player-title').textContent = episode.title || 'Untitled listen';
    renderRename();
    $('player-kicker').textContent = episode.status === 'complete' ? 'READY WHEN YOU ARE' : 'YOUR NEXT LISTEN';
    const voice = state.config?.voices?.find((item) => item.id === episode.voice)?.name || episode.voice || 'Local voice';
    const metadata = [voice];
    if (episode.word_count) metadata.push(`${Number(episode.word_count).toLocaleString()} words`);
    if (episode.duration) metadata.push(formatTime(episode.duration));
    $('player-meta').textContent = metadata.join(' · ');
    const ready = episode.status === 'complete';
    $('recast-button').hidden = !ready || !state.config?.features?.recast;
    $('ready-player').hidden = !ready; $('job-state').hidden = ready;
    const sourceUrl = safeSourceUrl(episode.source_url);
    $('source-link').hidden = !sourceUrl;
    if (sourceUrl) $('source-link').href = sourceUrl; else $('source-link').removeAttribute('href');
    if (!ready) {
      $('job-status').textContent = statusLabels[episode.status] || episode.status;
      $('job-percent').textContent = isPending(episode) ? `${progressOf(episode)}%` : '';
      $('job-progress').value = progressOf(episode); $('job-progress').hidden = !isPending(episode);
      const estimate = remainingTime(episode);
      $('job-eta').textContent = estimate; $('job-eta').hidden = !estimate;
      if (estimate) $('job-progress').setAttribute('aria-describedby', 'job-eta');
      else $('job-progress').removeAttribute('aria-describedby');
      $('cancel-button').hidden = !isPending(episode); $('retry-button').hidden = isPending(episode);
      const canEdit = ['failed', 'cancelled'].includes(episode.status) && typeof episode.text === 'string' && !!episode.text.trim();
      $('edit-episode-button').hidden = !canEdit; $('saved-text-hint').hidden = !canEdit;
      $('edit-episode-button').disabled = state.creating || state.importing;
      $('cancel-button').disabled = !!episode.cancel_requested;
      $('cancel-button').textContent = episode.cancel_requested ? 'Cancelling after this sentence…' : 'Cancel generation';
      $('job-message').textContent = episode.cancel_requested ? 'Finishing the current sentence, then stopping. This can take a moment.' : episode.status === 'queued' ? 'Waiting for the local voice. Your place in the queue is saved.' : episode.status === 'generating' ? 'Reading sentence by sentence, with space to breathe. You can keep using the app while this runs.' : episode.status === 'cancelled' ? 'Generation was cancelled. You can try again whenever you’re ready.' : episode.error || 'Audio could not be created. Check the local voice setup, then try again.';
      $('delete-button').hidden = isPending(episode);
      return;
    }
    $('delete-button').hidden = false;
    if (state.audioEpisodeId !== episode.id) {
      state.audioEpisodeId = episode.id;
      audio.src = apiPath(episode.id, '/audio.wav');
      updatePlaybackRate();
      audio.load();
    }
    $('download-audio').href = apiPath(episode.id, '/audio.wav');
    $('download-srt').href = apiPath(episode.id, '/transcript.srt');
    $('download-text').href = apiPath(episode.id, '/transcript.txt');
    $('duration-time').textContent = formatTime(audio.duration || episode.duration);
    const cues = Array.isArray(episode.cues) ? episode.cues : [];
    const signature = `${episode.id}:${cues.length}:${cues.at(-1)?.end || 0}`;
    if (state.cuesSignature !== signature) { state.cuesSignature = signature; renderTranscript(); }
  }

  function appendHighlighted(node, text, query) {
    if (!query) { node.textContent = text; return; }
    const lower = text.toLocaleLowerCase(); const needle = query.toLocaleLowerCase();
    let offset = 0; let index;
    while ((index = lower.indexOf(needle, offset)) !== -1) {
      node.append(document.createTextNode(text.slice(offset, index)), element('mark', '', text.slice(index, index + query.length)));
      offset = index + query.length;
    }
    node.append(document.createTextNode(text.slice(offset)));
  }

  function renderTranscript() {
    const container = $('transcript'); container.replaceChildren(); state.cueElements.clear(); state.currentCue = -1;
    const query = $('transcript-search').value.trim();
    const cues = state.selected?.cues || [];
    cues.forEach((cue, index) => {
      if (query && !cue.text.toLocaleLowerCase().includes(query.toLocaleLowerCase())) return;
      const button = element('button', 'transcript-cue'); button.type = 'button';
      button.setAttribute('aria-label', `Jump to ${formatTime(cue.start)}: ${cue.text}`);
      const text = element('span', 'cue-text'); appendHighlighted(text, cue.text, query);
      button.append(element('span', 'cue-time', formatTime(cue.start)), text);
      button.addEventListener('click', () => {
        stopVoicePreview();
        audio.currentTime = Math.max(0, Number(cue.start) || 0); updatePlayback();
        audio.play().catch((error) => toast(`Could not start playback: ${error.message}`, { error: true }));
      });
      state.cueElements.set(index, button); container.append(button);
    });
    if (!state.cueElements.size) container.append(element('p', 'no-results', query ? 'No sentences match that phrase.' : 'No transcript is available for this listen.'));
    updateCurrentCue(false);
  }

  function updateCurrentCue(allowScroll = true) {
    const cues = state.selected?.cues || []; const time = audio.currentTime;
    // Binary search remains fast for long articles and highlights the last sentence during its trailing pause.
    let low = 0; let high = cues.length - 1; let index = -1;
    while (low <= high) { const mid = (low + high) >> 1; if (Number(cues[mid].start) <= time) { index = mid; low = mid + 1; } else high = mid - 1; }
    if (index === state.currentCue) return;
    const previous = state.cueElements.get(state.currentCue);
    if (previous) { previous.classList.remove('current'); previous.removeAttribute('aria-current'); }
    state.currentCue = index;
    const current = state.cueElements.get(index);
    if (!current) return;
    current.classList.add('current'); current.setAttribute('aria-current', 'true');
    if (allowScroll && $('auto-follow').checked && !$('transcript-search').value.trim()) {
      const container = $('transcript');
      const top = current.getBoundingClientRect().top - container.getBoundingClientRect().top + container.scrollTop;
      const visibleTop = container.scrollTop; const visibleBottom = visibleTop + container.clientHeight;
      if (top < visibleTop || top + current.offsetHeight > visibleBottom) container.scrollTo({ top: Math.max(0, top - container.clientHeight / 3), behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' });
    }
  }

  function updatePlayback() {
    const duration = Number.isFinite(audio.duration) ? audio.duration : state.selected?.duration || 0;
    $('current-time').textContent = formatTime(audio.currentTime); $('duration-time').textContent = formatTime(duration);
    $('audio-seek').max = String(duration || 1); $('audio-seek').value = String(audio.currentTime || 0);
    $('audio-seek').setAttribute('aria-valuetext', `${formatTime(audio.currentTime)} of ${formatTime(duration)}`);
    updateCurrentCue(); savePosition();
  }

  function updatePlayButton() {
    const playing = !audio.paused;
    $('play-button').classList.toggle('playing', playing); $('play-button').setAttribute('aria-label', playing ? 'Pause' : 'Play');
    $('play-icon').replaceChildren(playbackIcon(playing ? 'pause' : 'play'));
  }

  function updatePlaybackRate(persist = false) {
    const rate = playbackRates[Number($('playback-rate').value)] ?? 1;
    audio.playbackRate = rate;
    $('playback-rate-value').textContent = `${rate}×`;
    $('playback-rate').setAttribute('aria-valuetext', `${rate} times normal speed`);
    if (persist) storage.set('playback-rate', String(rate));
  }

  async function changeEpisode(action) {
    const episode = state.selected; if (!episode) return;
    const button = $(action === 'cancel' ? 'cancel-button' : 'retry-button'); button.disabled = true;
    try {
      const titleRevision = state.titleRevision;
      const result = await api(apiPath(episode.id, `/${action}`), { method: 'POST', body: '{}' });
      if (state.selectedId === episode.id) { state.selected = preserveSavedTitle(result.episode || result, titleRevision); renderSelected(); }
      await refreshLibrary();
    } catch (error) { toast(error.message, { error: true }); }
    finally { button.disabled = action === 'cancel' && !!state.selected?.cancel_requested; }
  }

  function preserveSavedTitle(episode, requestRevision) {
    const saved = state.savedTitles.get(episode.id);
    // A read already in flight when a rename succeeds may still carry its old title.
    return saved && saved.revision > requestRevision ? { ...episode, title: saved.title } : episode;
  }

  function renderRename() {
    const editing = state.rename?.id === state.selectedId;
    const saving = editing && state.rename.saving;
    $('rename-form').hidden = !editing;
    $('rename-button').hidden = editing || !state.config?.features?.rename;
    $('rename-button').setAttribute('aria-expanded', String(editing));
    $('rename-title').disabled = saving;
    $('rename-save').disabled = saving;
    $('rename-cancel').disabled = saving;
    $('rename-save').textContent = saving ? 'Saving…' : 'Save title';
    $('rename-form').setAttribute('aria-busy', String(saving));
    // Do not set the input value here: polling must never replace an unfinished draft.
  }

  function renameError(message = '') {
    $('rename-error').textContent = message;
    $('rename-error').hidden = !message;
    $('rename-title').setAttribute('aria-invalid', String(!!message));
  }

  function beginRename() {
    if (!state.selected || !state.config?.features?.rename) return;
    state.rename = { id: state.selectedId, saving: false };
    $('rename-title').value = state.selected.title || '';
    renameError(); renderRename();
    $('rename-title').focus(); $('rename-title').select();
  }

  function closeRename(focus = false) {
    state.rename = null;
    renameError(); renderRename();
    if (focus) $('rename-button').focus();
  }

  async function saveRename(event) {
    event.preventDefault();
    const edit = state.rename;
    if (!edit || edit.saving || edit.id !== state.selectedId) return;
    const title = $('rename-title').value.replace(/\s+/gu, ' ').trim();
    if (!title || Array.from(title).length > 200) {
      renameError(!title ? 'Enter a title for this listen.' : 'Use a title of 200 characters or fewer.');
      $('rename-title').focus(); return;
    }
    edit.saving = true; renameError(); renderRename();
    try {
      const result = await api(apiPath(edit.id), { method: 'PATCH', body: JSON.stringify({ title }) });
      const savedTitle = (result.episode || result).title;
      state.savedTitles.set(edit.id, { title: savedTitle, revision: ++state.titleRevision });
      state.episodes = state.episodes.map((item) => item.id === edit.id ? { ...item, title: savedTitle } : item);
      // Only merge the title: generation and playback may have moved on during the save.
      if (state.selectedId === edit.id && state.selected) {
        state.selected = { ...state.selected, title: savedTitle };
        renderSelected();
      }
      renderLibrary();
      if (state.rename === edit) closeRename(true);
      toast('Title saved.');
    } catch (error) {
      if (state.rename === edit) { renameError(error.message); edit.saving = false; renderRename(); $('rename-title').focus(); }
      else toast(error.message, { error: true });
    }
  }

  function resetDeleteConfirmation() { state.deleteConfirm = false; $('delete-button').textContent = 'Delete this listen'; $('delete-button').classList.remove('confirm'); }
  async function deleteEpisode() {
    const id = state.selectedId; if (!id) return;
    if (!state.deleteConfirm) { state.deleteConfirm = true; $('delete-button').textContent = 'Delete audio & transcript? Click to confirm'; $('delete-button').classList.add('confirm'); setTimeout(resetDeleteConfirmation, 6000); return; }
    $('delete-button').disabled = true;
    try {
      await api(apiPath(id), { method: 'DELETE' });
      state.libraryRevision++;
      resetPlayer(); storage.remove(`position:${id}`); storage.remove('selected');
      state.selected = null; state.selectedId = null; state.selectionRequest++;
      state.episodes = state.episodes.filter((item) => item.id !== id); renderLibrary(); renderSelected();
      if (state.episodes.length) await selectEpisode(state.episodes[0].id);
      toast('Listen deleted.');
    } catch (error) { toast(error.message, { error: true }); }
    finally { $('delete-button').disabled = false; resetDeleteConfirmation(); }
  }

  function notifyComplete(episode) {
    toast(`“${episode.title || 'Your listen'}” is ready.`, { episodeId: episode.id });
    if ($('notifications-toggle').checked && 'Notification' in window && Notification.permission === 'granted') {
      try {
        const notification = new Notification('Your listen is ready', { body: episode.title || 'Audio and transcript are ready in Margin.', tag: `margin:${episode.id}` });
        notification.onclick = () => { window.focus(); selectEpisode(episode.id); notification.close(); };
      } catch { /* Some browsers grant permission without supporting desktop notifications. */ }
    }
  }

  async function refreshLibrary() {
    if (state.polling) return;
    state.polling = true;
    try {
      const titleRevision = state.titleRevision;
      const libraryRevision = state.libraryRevision;
      const result = await api('/api/episodes');
      // A poll started before a local creation must not hide its new shelf item.
      if (libraryRevision !== state.libraryRevision) return;
      const episodes = (Array.isArray(result) ? result : result.episodes || []).map((episode) => preserveSavedTitle(episode, titleRevision));
      for (const episode of episodes) {
        const previous = state.previousStatuses.get(episode.id);
        if (state.loaded && previous && previous !== 'complete' && episode.status === 'complete') notifyComplete(episode);
        if (state.loaded && previous && previous !== 'failed' && episode.status === 'failed') toast(`“${episode.title || 'Your listen'}” could not be created. Select it for details.`, { error: true });
        state.previousStatuses.set(episode.id, episode.status);
      }
      state.episodes = episodes; renderLibrary();
      if (!state.loaded) {
        state.loaded = true;
        const saved = storage.get('selected');
        const first = episodes.find((item) => item.id === saved) || episodes[0];
        if (first) await selectEpisode(first.id);
      } else if (state.selectedId) {
        const summary = episodes.find((item) => item.id === state.selectedId);
        if (!summary) {
          resetPlayer(); state.selected = null; state.selectedId = null; state.selectionRequest++; renderSelected();
        } else {
          if (state.selected && state.selected.title !== summary.title) {
            state.selected = { ...state.selected, title: summary.title }; renderSelected();
          }
          if (!state.selected || isPending(state.selected) || state.selected.status !== summary.status) {
            const id = state.selectedId; const selectionRequest = state.selectionRequest; const detailRevision = state.titleRevision;
            const detail = await api(apiPath(id));
            if (id === state.selectedId && selectionRequest === state.selectionRequest) { state.selected = preserveSavedTitle(detail.episode || detail, detailRevision); renderSelected(); }
          }
        }
      }
      if (state.config?.engine?.ready) { $('engine-status').classList.remove('offline'); $('engine-status').classList.add('ready'); $('engine-label').textContent = 'Local voice connected'; }
    } catch (error) {
      $('engine-status').classList.add('offline'); $('engine-status').classList.remove('ready'); $('engine-label').textContent = 'Local server disconnected';
      if (!state.loaded) { $('library').replaceChildren(element('div', 'library-empty', error.message)); }
    } finally { state.polling = false; }
  }

  async function configure() {
    try {
      state.config = await api('/api/config');
      const engine = state.config.engine || {};
      $('engine-status').classList.toggle('ready', !!engine.ready);
      $('engine-label').textContent = engine.ready ? 'Local voice connected' : 'Local voice needs setup';
      $('engine-status').title = engine.name || 'Local text-to-speech';
      $('engine-message').hidden = engine.ready || !engine.message;
      $('engine-message').textContent = engine.message || '';
      $('desktop-notification-help').hidden = !state.config.desktop_notifications;
      $('desktop-notification-help').textContent = 'The local server can also send desktop alerts with this tab closed, while the server and your desktop session are running.';
      state.config.voices = (state.config.voices || []).map(normalizeVoice);
      const storedVoice = storage.get('voice');
      const selectedVoice = state.config.voices.find((voice) => voice.id === storedVoice)
        || state.config.voices.find((voice) => voice.id === state.config.defaults?.voice)
        || state.config.voices[0];
      $('voice-select').value = selectedVoice?.id || '';
      buildVoiceCards();
      const defaultSpeed = String(state.config.defaults?.speed || 1);
      if (Array.from($('pace-select').options).some((option) => option.value === defaultSpeed)) $('pace-select').value = defaultSpeed;
      updateForm(); stopVoicePreview();
    } catch (error) { $('engine-status').classList.add('offline'); $('engine-label').textContent = 'Local server disconnected'; formError(error.message); renderVoicePreview(); }
  }

  $('text-tab').addEventListener('click', () => setTab('text'));
  $('url-tab').addEventListener('click', () => setTab('url'));
  for (const tab of [$('text-tab'), $('url-tab')]) tab.addEventListener('keydown', (event) => { if (['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) { event.preventDefault(); setTab(event.key === 'Home' ? 'text' : event.key === 'End' ? 'url' : tab.id === 'text-tab' ? 'url' : 'text', true); } });
  $('article-text').addEventListener('input', updateForm); $('pace-select').addEventListener('change', updateForm);
  $('voice-preview-button').addEventListener('click', () => toggleVoicePreview());
  $('voice-picker-button').addEventListener('click', () => openVoicePicker());
  $('voice-picker-close').addEventListener('click', closeVoicePicker);
  $('voice-picker-cancel').addEventListener('click', closeVoicePicker);
  $('voice-picker-done').addEventListener('click', () => voiceBrowser.recast ? submitRecast() : closeVoicePicker());
  $('recast-button').addEventListener('click', beginRecast);
  $('voice-picker').addEventListener('cancel', (event) => { if (voiceBrowser.recast?.saving) event.preventDefault(); });
  $('voice-picker').addEventListener('close', () => {
    const wasRecasting = !!voiceBrowser.recast;
    voiceBrowser.recast = null;
    document.body.classList.remove('voice-picker-open');
    stopVoicePreview();
    renderVoiceSelection();
    const returnFocus = wasRecasting && !$('recast-button').hidden ? $('recast-button') : $('voice-picker-button');
    returnFocus.focus({ preventScroll: true });
  });
  $('voice-search').addEventListener('input', filterVoiceCards);
  for (const button of document.querySelectorAll('[data-voice-accent]')) button.addEventListener('click', () => { voiceBrowser.accent = button.dataset.voiceAccent; filterVoiceCards(); });
  for (const button of document.querySelectorAll('[data-voice-gender]')) button.addEventListener('click', () => { voiceBrowser.gender = button.dataset.voiceGender; filterVoiceCards(); });
  $('voice-favorites-filter').addEventListener('click', () => { voiceBrowser.favoritesOnly = !voiceBrowser.favoritesOnly; filterVoiceCards(); });
  $('voice-reset-filters').addEventListener('click', () => {
    voiceBrowser.accent = 'all'; voiceBrowser.gender = 'all'; voiceBrowser.favoritesOnly = false;
    $('voice-search').value = ''; filterVoiceCards(); $('voice-search').focus();
  });
  $('import-button').addEventListener('click', importArticle);
  $('source-url').addEventListener('keydown', (event) => { if (event.key === 'Enter') { event.preventDefault(); importArticle(); } });
  $('episode-form').addEventListener('submit', createEpisode);
  $('sample-button').addEventListener('click', () => {
    $('episode-title').value = 'The pleasure of paying attention';
    $('article-text').value = 'There is a particular kind of quiet that arrives when you put your phone away and step outside. At first, you might not notice it. The street is still busy. Someone is talking. A bus sighs at the corner.\n\nBut then you hear something else: the soft scrape of leaves along the pavement, or a bird trying out the same three notes. The world has not become quieter. You have simply started listening.\n\nReading can work that way, too. A good sentence asks us to slow down. To make a little room. And sometimes, hearing it aloud is all it takes to find something we missed the first time.';
    state.sourceUrl = ''; $('import-note').hidden = true; formError(); updateForm(); $('article-text').focus();
  });
  $('play-button').addEventListener('click', () => { if (audio.paused) { stopVoicePreview(); audio.play().catch((error) => toast(`Could not start playback: ${error.message}`, { error: true })); } else audio.pause(); });
  $('back-button').addEventListener('click', () => { audio.currentTime = Math.max(0, audio.currentTime - 15); updatePlayback(); });
  $('forward-button').addEventListener('click', () => { audio.currentTime = Math.min(audio.duration || 0, audio.currentTime + 15); updatePlayback(); });
  $('audio-seek').addEventListener('input', () => { audio.currentTime = Number($('audio-seek').value); updatePlayback(); });
  $('playback-rate').addEventListener('input', () => updatePlaybackRate(true));
  $('transcript-search').addEventListener('input', renderTranscript);
  $('auto-follow').addEventListener('change', () => { storage.set('auto-follow', $('auto-follow').checked); state.currentCue = -1; updateCurrentCue(); });
  $('cancel-button').addEventListener('click', () => changeEpisode('cancel'));
  $('retry-button').addEventListener('click', () => changeEpisode('retry'));
  $('edit-episode-button').addEventListener('click', () => restoreEpisodeToEditor());
  $('restore-dialog').addEventListener('close', () => {
    const episode = restoreSource;
    restoreSource = null;
    if ($('restore-dialog').returnValue === 'restore' && episode) restoreEpisodeToEditor(episode, true);
    else $('article-text').focus();
  });
  $('delete-button').addEventListener('click', deleteEpisode);
  $('rename-button').addEventListener('click', beginRename);
  $('rename-form').addEventListener('submit', saveRename);
  $('rename-cancel').addEventListener('click', () => closeRename(true));
  $('rename-title').addEventListener('input', () => renameError());
  $('rename-form').addEventListener('keydown', (event) => { if (event.key === 'Escape' && !state.rename?.saving) { event.preventDefault(); closeRename(true); } });
  audio.addEventListener('timeupdate', updatePlayback);
  audio.addEventListener('play', () => { if (!audio.paused) stopVoicePreview(); updatePlayButton(); });
  audio.addEventListener('pause', () => { updatePlayButton(); savePosition(true); });
  audio.addEventListener('ended', () => { updatePlayButton(); savePosition(true); });
  audio.addEventListener('loadedmetadata', () => {
    const saved = Number(storage.get(`position:${state.audioEpisodeId}`, 0));
    if (saved > 0 && saved < audio.duration - 1) audio.currentTime = saved;
    updatePlaybackRate(); updatePlayback();
  });
  audio.addEventListener('error', () => { if (state.audioEpisodeId) toast('This audio could not be loaded. Check that the local server is running.', { error: true }); });
  window.addEventListener('pagehide', () => { savePosition(true); stopVoicePreview(); });
  document.addEventListener('visibilitychange', () => { if (document.hidden) savePosition(true); else refreshLibrary(); });

  const savedRate = Number(storage.get('playback-rate', '1'));
  $('playback-rate').max = String(playbackRates.length - 1);
  $('playback-rate').value = String(playbackRates.includes(savedRate) ? playbackRates.indexOf(savedRate) : playbackRates.indexOf(1));
  $('playback-rate-notches').replaceChildren(...playbackRates.map((rate) => {
    const notch = element('span');
    notch.append(element('span', '', `${rate}×`));
    return notch;
  }));
  updatePlaybackRate();
  $('auto-follow').checked = storage.get('auto-follow', 'true') !== 'false';
  const notificationsSupported = window.isSecureContext && 'Notification' in window;
  $('notifications-toggle').checked = notificationsSupported && Notification.permission === 'granted' && storage.get('notifications') === 'true';
  $('notifications-toggle').disabled = !notificationsSupported;
  if (!notificationsSupported) $('notification-help').textContent = window.isSecureContext ? 'Desktop notifications are not available in this browser. Completion messages still appear here while this page is open.' : 'Browser notifications require HTTPS. Completion messages still appear here; server desktop alerts appear on the machine running Margin.';
  $('notifications-toggle').addEventListener('change', async () => {
    if ($('notifications-toggle').checked) {
      try {
        const permission = await Notification.requestPermission();
        if (permission !== 'granted') { $('notifications-toggle').checked = false; toast('Notifications are blocked. You can enable them in your browser’s site settings.'); }
      } catch { $('notifications-toggle').checked = false; toast('This browser could not enable desktop notifications.'); }
    }
    storage.set('notifications', $('notifications-toggle').checked);
  });

  configure().then(refreshLibrary);
  setInterval(async () => { if (!state.config) await configure(); await refreshLibrary(); }, 2500);
})();
