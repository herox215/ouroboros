// One page owns one poll loop. Disposing it invalidates in-flight responses as
// well as clearing the timer, so neither navigation nor another profile leaks.
export function mountSpeechSettings(bridge, profile, host, doc, timers = globalThis) {
  let alive = true, timer, pending = false, snapshot, saved, request = 0;
  const el = (tag, text = '') => { const node = doc.createElement(tag); node.textContent = text; return node; };
  const form = el('form'); form.className = 'speech-settings'; host.append(form);
  const field = (text, tag = 'input') => {
    const label = el('label', text), input = el(tag); label.append(input); form.append(label); return [input, label];
  };
  const [provider] = field('Provider', 'select');
  for (const value of ['disabled', 'local', 'remote']) {
    const option = el('option', value); option.value = value; provider.append(option);
  }
  const [localModel, localLabel] = field('Local model', 'select');
  const [endpoint, endpointLabel] = field('Remote endpoint'); endpoint.type = 'url';
  const [remoteModel, remoteLabel] = field('Remote model');
  const [key, keyLabel] = field('API key (blank keeps saved key)'); key.type = 'password'; key.autocomplete = 'new-password';
  const [language] = field('Language (blank = automatic)');
  const disclosure = el('p', 'Remote recognition sends your audio off this computer to the configured provider.'); form.append(disclosure);
  const keyStatus = el('p'); form.append(keyStatus);
  const state = el('p', 'Loading …'); state.setAttribute('role', 'status'); form.append(state);
  const progress = el('progress'); progress.max = 1; progress.value = 0;
  progress.setAttribute('aria-label', 'Speech setup download'); form.append(progress);
  const bytes = el('p'); form.append(bytes);
  const setupNote = el('p', 'Save changes before downloading. Download / setup uses the saved local configuration; opening or saving settings never starts a download.'); form.append(setupNote);
  const message = el('p'); message.setAttribute('role', 'status'); form.append(message);
  const action = (text, run) => { const node = el('button', text); node.type = 'button'; node.addEventListener('click', run); form.append(node); return node; };
  const save = action('Save', () => operate('save'));
  const download = action('Download / setup', () => operate('download'));
  const cancel = action('Cancel download', () => operate('cancel'));
  const values = () => ({ provider: provider.value, model: provider.value === 'remote' ? remoteModel.value : localModel.value,
    endpoint: endpoint.value, api_key: key.value, language: language.value });
  const dirty = () => !saved || Object.keys(saved).some(k => values()[k] !== saved[k]);
  const update = () => {
    const local = provider.value === 'local', remote = provider.value === 'remote';
    setupNote.hidden = !local;
    localLabel.hidden = !local;
    endpointLabel.hidden = remoteLabel.hidden = keyLabel.hidden = disclosure.hidden = keyStatus.hidden = !remote;
    save.disabled = pending || !snapshot;
    for (const input of [provider, localModel, endpoint, remoteModel, key, language]) input.disabled = pending || !snapshot;
    download.hidden = !local;
    download.disabled = pending || !snapshot || dirty() || !!snapshot.download?.active;
    cancel.hidden = !snapshot?.download?.active; cancel.disabled = pending;
    download.title = dirty() ? 'Save your changes before downloading.' : 'Set up the saved local model';
  };
  for (const input of [provider, localModel, endpoint, remoteModel, key, language]) {
    input.addEventListener('input', update); input.addEventListener('change', update);
  }
  const render = value => {
    snapshot = value;
    if (!saved) {
      const config = value.config || {};
      provider.value = config.provider || 'disabled';
      const models = value.models?.length ? value.models : [{ id: 'large-v3-turbo-q5_0', label: 'large-v3-turbo-q5_0' }];
      for (const model of models) {
        const option = el('option', model.label || model.id); option.value = model.id; localModel.append(option);
      }
      const selected = config.provider === 'remote' ? models[0].id : config.model || 'large-v3-turbo-q5_0';
      if (!models.some(model => model.id === selected)) {
        const option = el('option', selected); option.value = selected; localModel.append(option);
      }
      localModel.value = selected; remoteModel.value = config.provider === 'remote' ? config.model || '' : '';
      endpoint.value = config.endpoint || ''; language.value = config.language || ''; key.value = '';
      saved = values();
    }
    keyStatus.textContent = value.has_key ? 'An API key is saved.' : 'No API key saved.';
    // Backend status/error text can contain URLs or credentials. Only display
    // our fixed descriptions and known phases, never arbitrary error strings.
    state.textContent = ({ disabled: 'Speech recognition is disabled.', remote: 'Remote endpoint configured (not tested).',
      missing_ffmpeg: 'Local recognition needs FFmpeg installed on this computer.',
      missing_runtime: 'Whisper runtime is missing. Use Download / setup.',
      missing_model: 'Local model is missing. Use Download / setup.',
      downloading: 'Local setup is in progress.', error: 'Local setup failed. Check prerequisites and retry.' })[value.status]
      || (value.runtime_ready && value.model_ready ? 'Saved local configuration: runtime and model available (recognition not tested).'
        : `Saved local configuration · Runtime: ${value.runtime_ready ? 'ready' : 'not ready'} · Model: ${value.model_ready ? 'ready' : 'not ready'}`);
    const d = value.download || {};
    const received = Math.max(0, Number(d.received) || 0), total = Math.max(0, Number(d.total) || 0);
    const phase = ({ starting: 'Starting setup', runtime_download: 'Downloading runtime', runtime_build: 'Building runtime', downloading: 'Downloading', download: 'Downloading', runtime: 'Setting up runtime', model: 'Downloading model', extracting: 'Extracting', verifying: 'Verifying', complete: 'Complete', cancelled: 'Cancelled', error: 'Failed' })[d.phase] || (d.active ? 'Setup in progress' : 'Idle');
    progress.max = total || 1; progress.value = total ? Math.min(received, total) : 0;
    bytes.textContent = `${phase} · ${received} bytes${total ? ` / ${total} bytes (${Math.min(100, Math.floor(received / total * 100))}%)` : ' · total unknown'}`;
    if (d.error) message.textContent = 'Speech setup failed. Check your settings and try again.';
    update();
  };
  const refresh = async () => {
    const current = ++request;
    try { const value = await bridge.SpeechSettings(profile); if (alive && current === request) render(value); }
    catch { if (alive && current === request) message.textContent = 'Could not read speech settings. Try again.'; }
  };
  const poll = async () => {
    if (!pending) await refresh();
    if (alive) timer = timers.setTimeout(poll, 1000);
  };
  const operate = async kind => {
    if (!alive || pending || !snapshot) return;
    if (kind === 'download' && dirty()) { message.textContent = 'Save your changes before downloading.'; return; }
    pending = true; request++; message.textContent = ''; update();
    const change = values(); key.value = '';
    try {
      if (kind === 'save') await bridge.SaveSpeech(profile, change);
      else if (kind === 'download') await bridge.DownloadSpeech(profile);
      else await bridge.CancelSpeechDownload(profile);
      if (!alive) return;
      if (kind === 'save') { saved = { ...change, api_key: '' }; message.textContent = 'Saved. Local setup requires the Download / setup button.'; }
      await refresh();
    } catch { if (alive) message.textContent = 'Speech operation failed. Check your settings and try again.'; }
    finally { change.api_key = ''; if (alive) { pending = false; update(); } }
  };
  form.addEventListener('submit', event => { event.preventDefault(); operate('save'); });
  update(); poll();
  return () => { alive = false; timers.clearTimeout(timer); key.value = ''; };
}
