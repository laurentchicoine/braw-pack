'use strict';

// ---------- backend ----------
// Inside the app, Wails provides window.go and window.runtime. Opened in a plain browser (for
// working on the layout) a small stand-in with sample data is used instead.
const inApp = !!(window.go && window.go.main);
const api = inApp ? window.go.main.App : mockApi();
const on = (name, fn) => inApp ? window.runtime.EventsOn(name, fn) : mockEvents.on(name, fn);

// ---------- state ----------
const state = {
  tab: 'compress',
  lists: { compress: [], decompress: [] },   // items, each with a .job {status, stage, fraction, message, output}
  selected: null,                            // path of the selected clip (compress tab)
  // quality 0 lets each clip's level be chosen from the clip; manualQuality is the slider's level.
  // layers 0 lets each clip's layers be chosen from the clip (GPU only: the CPU always stores one).
  settings: { encoder: 'nvenc', quality: 0, layers: 0, speed: 'balanced', intra: false, output: 'archive', ratio: 12, checkAll: true, lut: false },
  manualQuality: 10,
  ratios: [],                                // compression ratios offered for "Smaller BRAW"
  rebuild: 'auto',
  running: false,
  scanning: null,                            // {tab, done, total} while added files are being read; total < 0 until they are counted
  estimate: null,                           // last estimate result
  estimating: false,
  notice: '',                                // message shown in the footer until the next action
  estimateFraction: 0,
  tools: null,
};

// The folder last saved to, per tab, is remembered so the folder picker can start there. It is a
// convenience only: storage may be unavailable, and the picker then starts wherever the system likes.
function lastDir(tab) {
  try { return localStorage.getItem('outDir.' + tab) || ''; } catch (e) { return ''; }
}
function rememberDir(tab, dir) {
  try { localStorage.setItem('outDir.' + tab, dir); } catch (e) { /* not remembered */ }
}

const $ = (id) => document.getElementById(id);
const el = (tag, cls, text) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
};

// ---------- formatting ----------
function bytes(n) {
  if (!n) return '';
  if (n >= 2 ** 30) return (n / 2 ** 30).toFixed(2) + ' GB';
  if (n >= 2 ** 20) return (n / 2 ** 20).toFixed(n >= 100 * 2 ** 20 ? 0 : 1) + ' MB';
  return Math.max(1, Math.round(n / 1024)) + ' KB';
}
const ratio = (r) => r ? (r >= 20 ? r.toFixed(0) : r.toFixed(1)) + ':1' : '';
const fps = (f) => f ? (Number.isInteger(f) ? f : f.toFixed(2)) + ' fps' : '';
function duration(s) {
  if (s < 60) return s + ' s';
  return Math.floor(s / 60) + ' min ' + String(s % 60).padStart(2, '0') + ' s';
}
const stageNames = { analyse: 'Analysing', encode: 'Compressing', verify: 'Checking', finish: 'Finishing', rebuild: 'Rebuilding' };

// ---------- rendering ----------
const items = () => state.lists[state.tab];

function metaLine(it) {
  if (it.error) return it.error;
  const parts = [bytes(it.size)];
  if (it.width) parts.push(it.width + '×' + it.height);
  if (it.fps) parts.push(fps(it.fps));
  if (state.tab === 'compress') {
    if (it.ratio) parts.push('recorded at ' + ratio(it.ratio));
  } else {
    const predicted = state.rebuild === 'source' ? it.sourceQSize : it.autoSize;
    if (predicted) parts.push('rebuilds to about ' + bytes(predicted));
    else if (it.sourceSize) parts.push('original was ' + bytes(it.sourceSize));
  }
  return parts.filter(Boolean).join('  ·  ');
}

function renderList() {
  const list = $('list');
  list.textContent = '';
  for (const it of items()) {
    const row = el('div', 'item' + (state.tab === 'compress' && it.path === state.selected ? ' selected' : ''));
    row.title = it.path;
    row.append(el('div', 'name', it.name), statusCell(it), el('div', 'meta', metaLine(it)));
    const job = it.job;
    if (job && job.status === 'running') {
      const bar = el('div', 'bar');
      const fill = el('i');
      fill.style.width = Math.round(100 * (job.fraction || 0)) + '%';
      bar.append(fill);
      row.append(bar);
    }
    if (state.tab === 'compress') {
      row.addEventListener('click', () => { state.selected = it.path; render(); });
    }
    list.append(row);
  }
  const scan = state.scanning && state.scanning.tab === state.tab ? state.scanning : null;
  if (scan) renderScan(list, scan);
  const none = items().length === 0 && !scan;
  $('empty').hidden = !none;
  $('emptyTitle').textContent = state.tab === 'compress' ? 'Add Blackmagic RAW clips to compress' : 'Add archives to turn back into Blackmagic RAW';
  $('emptyText').textContent = state.tab === 'compress'
    ? 'Drop clips or folders here, or use the buttons above. Each clip becomes one .braw.mkv file holding the picture, the sound and everything needed to rebuild the clip. Your original files are never changed or deleted.'
    : 'Drop archives or folders here, or use the buttons above. Each .braw.mkv archive is rebuilt into a .braw clip that opens in DaVinci Resolve with its camera settings, sound and motion data.';
}

// While added files are being read, the list ends with a progress line and placeholder rows.
function renderScan(list, scan) {
  const what = scan.tab === 'compress' ? 'clip' : 'archive';
  const counted = scan.total >= 0;
  const head = el('div', 'scan');
  head.append(el('div', '', counted
    ? 'Reading ' + what + (scan.total > 1 ? 's' : '') + '… ' + scan.done + ' of ' + scan.total
    : 'Looking for ' + what + 's…'));
  const bar = el('div', 'bar' + (counted ? '' : ' unknown'));
  const fill = el('i');
  if (counted) fill.style.width = Math.round(100 * scan.done / Math.max(1, scan.total)) + '%';
  bar.append(fill);
  head.append(bar);
  list.append(head);
  // One placeholder per file found, each the size of the row that will take its place.
  const rows = counted ? scan.total : 3;
  for (let k = 0; k < rows; k++) {
    const row = el('div', 'item skeleton');
    const name = el('div', 'name'), meta = el('div', 'meta');
    name.append(el('i'));
    meta.append(el('i'));
    row.append(name, meta);
    list.append(row);
  }
}

function statusCell(it) {
  const cell = el('div', 'status');
  const job = it.job;
  if (!job) {
    if (!state.running) {
      const x = el('button', 'remove', '×');
      x.title = 'Remove from the list';
      x.addEventListener('click', (e) => {
        e.stopPropagation();
        state.lists[state.tab] = items().filter((o) => o !== it);
        if (state.selected === it.path) state.selected = null;
        render();
      });
      cell.append(x);
    }
    return cell;
  }
  if (job.status === 'running') {
    cell.textContent = (stageNames[job.stage] || 'Working') + ' ' + Math.round(100 * (job.fraction || 0)) + '%';
  } else if (job.status === 'waiting') {
    cell.textContent = 'Waiting';
  } else {
    cell.classList.add(job.status);
    let text = job.message || '';
    if (job.status === 'ok' && job.size) text = bytes(job.size) + ' · ' + text;
    if (job.status === 'ok' && job.seconds) text += ' · ' + duration(job.seconds);
    cell.textContent = text;
    if (job.output) {
      const show = el('button', '', 'Show');
      show.addEventListener('click', (e) => { e.stopPropagation(); api.Reveal(job.output); });
      cell.append(show);
    }
  }
  return cell;
}

function renderSettings() {
  for (const b of document.querySelectorAll('nav button')) b.setAttribute('aria-selected', b.dataset.tab === state.tab);
  $('compressSettings').hidden = state.tab !== 'compress';
  $('decompressSettings').hidden = state.tab !== 'decompress';
  for (const b of $('encoder').children) b.setAttribute('aria-pressed', b.dataset.value === state.settings.encoder);
  for (const b of $('speed').children) b.setAttribute('aria-pressed', b.dataset.value === state.settings.speed);
  const braw = state.settings.output === 'braw';
  for (const b of $('output').children) { b.setAttribute('aria-pressed', b.dataset.value === state.settings.output); b.disabled = state.running; }
  $('outputHint').textContent = braw
    ? 'A normal .braw file, compressed harder, saved under the same name in another folder. Opens anywhere Blackmagic RAW does; nothing to unpack later.'
    : 'A .braw.mkv file, far smaller than any BRAW. Decompress it to get a working .braw back.';
  $('archiveSettings').hidden = braw;
  $('shrinkSettings').hidden = !braw;
  renderRatios();
  const auto = !state.settings.quality;
  const est = state.estimate && state.estimate.kind === 'archive' && state.estimate.path === state.selected ? state.estimate : null;
  $('qualityAuto').checked = auto;
  $('quality').value = state.settings.quality || state.manualQuality;
  $('qualityValue').textContent = !auto ? state.settings.quality : est ? 'Auto (' + est.autoQuality + ')' : 'Auto';
  $('encoderHint').textContent = state.settings.encoder === 'nvenc'
    ? 'Fast. Stores the picture at 10 bits, or 10 bits plus a detail layer (see Layers).'
    : 'Much slower. Stores the picture at 12 bits. Works without an NVIDIA card.';
  const gpu = state.settings.encoder === 'nvenc';
  for (const b of $('layers').children) {
    b.setAttribute('aria-pressed', gpu ? Number(b.dataset.value) === state.settings.layers : b.dataset.value === '1');
    b.disabled = state.running || !gpu;
  }
  $('layersValue').textContent = !gpu ? '1' : state.settings.layers ? state.settings.layers : est ? 'Auto (' + est.layers + ')' : 'Auto';
  $('layersHint').textContent = gpu
    ? 'Two layers store the 10-bit picture plus a detail layer that preserves finer differences. Automatic uses two for all RGBW recordings (URSA Cine 12K and 17K), including scaled recordings, and one for other cameras. Two layers can make larger archives. With two layers the quality level is the detail layer’s.'
    : 'The CPU encoder always stores one layer, at 12 bits.';
  const t = state.tools;
  if (t) {
    for (const b of $('encoder').children) b.disabled = state.running || (b.dataset.value === 'nvenc' ? !t.NVENC : !t.X265);
  }

  const sel = state.lists.compress.find((it) => it.path === state.selected && !it.error);
  $('estimate').disabled = !sel || state.running || state.estimating;
  renderEstimateProgress();
  $('estimateHint').textContent = braw
    ? (sel ? 'Measures each ratio on sample frames of “' + sel.name + '”. Takes a few seconds.' : 'Select a clip, then estimate to see what each ratio gives.')
    : sel
      ? 'Tries each quality level on short stretches of “' + sel.name + '”. Takes about a minute.'
      : 'Select a clip, then estimate to see what each quality level gives.';
  renderEstimate();
}

// A ratio is offered only when it is clearly stronger than the selected clip's own.
const ratioAllowed = (r, clip) => !clip || !clip.ratio || r >= clip.ratio * 1.1;

function renderRatios() {
  const box = $('ratio');
  const sel = state.lists.compress.find((it) => it.path === state.selected && !it.error);
  const allowed = state.ratios.filter((r) => ratioAllowed(r, sel));
  if (allowed.length && !allowed.includes(state.settings.ratio)) {
    state.settings.ratio = allowed.find((r) => r > state.settings.ratio) || allowed[allowed.length - 1];
  }
  box.textContent = '';
  for (const r of state.ratios) {
    const b = el('button', '', r + ':1');
    b.setAttribute('aria-pressed', r === state.settings.ratio);
    b.disabled = state.running || !ratioAllowed(r, sel);
    b.addEventListener('click', () => { state.settings.ratio = r; render(); });
    box.append(b);
  }
  $('ratioHint').textContent = (sel && sel.ratio ? '“' + sel.name + '” was recorded at ' + ratio(sel.ratio) + '. ' : '') +
    'Higher numbers give smaller files and lose more fine detail. Clips already at or close to the chosen ratio are skipped. Blackmagic RAW cannot go much past 25:1; for smaller, make an archive.';
}

function renderEstimateProgress() {
  const pct = Math.round(100 * state.estimateFraction);
  $('estimate').textContent = state.estimating ? 'Estimating… ' + pct + '%' : 'Estimate selected clip';
  $('estimateBar').hidden = !state.estimating;
  $('estimateBar').firstElementChild.style.width = pct + '%';
}

function renderEstimate() {
  const out = $('estimateOut');
  out.textContent = '';
  const e = state.estimate;
  if (!e || state.tab !== 'compress' || e.kind !== state.settings.output) return;
  // The estimate was measured on one clip. Shown for another, its sizes are scaled to that clip.
  const sel = state.lists.compress.find((it) => it.path === state.selected && !it.error);
  const other = sel && sel.path !== e.path ? sel : null;
  const scale = other ? other.size / e.sourceBytes : 1;
  const scaled = other ? ' Measured on “' + e.name + '”; sizes are scaled to “' + other.name + '” (' + bytes(other.size) + ').' : '';
  if (e.kind === 'braw') { renderShrinkPlan(out, e, other, scaled); return; }
  const table = el('table');
  const head = el('tr');
  for (const h of ['Level', 'Archive', 'Detail kept', 'Unpacks to']) head.append(el('th', '', h));
  const thead = el('thead');
  thead.append(head);
  table.append(thead);
  const body = el('tbody');
  const current = state.settings.quality || e.autoQuality;
  for (const r of e.rows) {
    const tr = el('tr', r.quality === current ? 'current' : '');
    const archive = el('td', '', bytes(r.archiveBytes * scale) + ' ');
    archive.append(el('small', '', Math.round(100 * r.archiveBytes / e.sourceBytes) + '%'));
    const rebuilt = el('td', '', bytes(r.rebuiltBytes * scale) + ' ');
    rebuilt.append(el('small', '', ratio(r.rebuiltRatio)));
    tr.append(el('td', '', r.quality === e.autoQuality ? r.quality + ' auto' : r.quality), archive, el('td', '', r.detailKept.toFixed(1) + '%'), rebuilt);
    tr.title = 'Use quality level ' + r.quality;
    tr.addEventListener('click', () => { state.settings.quality = state.manualQuality = r.quality; render(); });
    body.append(tr);
  }
  table.append(body);
  const mode = (e.intra ? 'keeping sensor noise' : 'smallest files') + (e.layers === 2 ? ', two layers (the levels are the detail layer’s)' : '');
  out.append(table, el('p', 'hint',
    'For “' + e.name + '” (' + bytes(e.sourceBytes) + ', recorded at ' + ratio(e.sourceRatio) + '), ' + mode +
    '. “Auto” marks the level chosen for this clip when the quality level is chosen for each clip. “Detail kept” is the share of the original’s fine picture detail still in the archive; sensor noise is mostly left out of it. A lower level does not always keep more, so check the table. “Unpacks to” is the clip you get back, with its ratio against unprocessed sensor data.' + scaled));
}

function renderShrinkPlan(out, e, other, scaled) {
  const table = el('table');
  const head = el('tr');
  for (const h of ['Ratio', 'Size', 'Detail kept']) head.append(el('th', '', h));
  const thead = el('thead');
  thead.append(head);
  table.append(thead);
  const body = el('tbody');
  for (const r of e.rows || []) {
    // For another clip the size follows from its own recorded ratio; a ratio it already has is not on offer.
    const usable = ratioAllowed(r.ratio, other);
    const share = other && other.ratio ? other.ratio / r.ratio : r.bytes / e.sourceBytes;
    const tr = el('tr', !usable ? 'off' : r.ratio === state.settings.ratio ? 'current' : '');
    const size = el('td', '', usable ? bytes((other ? other.size : e.sourceBytes) * share) + ' ' : 'already there');
    if (usable) size.append(el('small', '', Math.round(100 * share) + '%'));
    tr.append(el('td', '', r.ratio + ':1'), size, el('td', '', r.detailKept.toFixed(1) + '%'));
    if (usable) {
      tr.title = 'Use ' + r.ratio + ':1';
      tr.addEventListener('click', () => { state.settings.ratio = r.ratio; render(); });
    }
    body.append(tr);
  }
  table.append(body);
  out.append(table, el('p', 'hint',
    'For “' + e.name + '” (' + bytes(e.sourceBytes) + ', recorded at ' + ratio(e.sourceRatio) + '). As Blackmagic RAW this clip stops getting smaller near ' +
    ratio(e.maxRatio) + '. “Detail kept” is the share of the original’s fine picture detail still in the new clip.' + scaled));
}

function renderFooter() {
  const list = items().filter((it) => !it.error);
  const total = list.reduce((a, it) => a + it.size, 0);
  let text = '';
  if (list.length) {
    text = list.length + (state.tab === 'compress' ? ' clip' : ' archive') + (list.length > 1 ? 's' : '') + ', ' + bytes(total);
    if (state.tab === 'compress') {
      const e = state.estimate;
      const braw = state.settings.output === 'braw';
      if (braw) {
        // Picture data dominates a clip, so its size follows the ratio closely.
        const target = state.settings.ratio;
        const todo = list.filter((it) => it.ratio && ratioAllowed(target, it));
        const after = todo.reduce((a, it) => a + it.size * it.ratio / target, 0);
        const skipped = list.length - todo.length;
        if (todo.length) text += '  →  about ' + bytes(after + total - todo.reduce((a, it) => a + it.size, 0));
        if (skipped) text += ' (' + skipped + ' already at or close to ' + target + ':1)';
      }
      const row = e && !braw && e.kind === 'archive' && e.rows.find((r) => r.quality === (state.settings.quality || e.autoQuality));
      if (row && e.intra === state.settings.intra) {
        text += '  →  roughly ' + bytes(total * row.archiveBytes / e.sourceBytes) + ' if the other clips behave like the estimated one';
      }
    } else {
      const sizes = list.map((it) => state.rebuild === 'source' ? it.sourceQSize : it.autoSize);
      if (sizes.every(Boolean)) text += '  →  about ' + bytes(sizes.reduce((a, b) => a + b, 0));
    }
  }
  if (state.notice) text = state.notice;
  const t = state.tools;
  if (t && !t.FFmpeg) text = 'ffmpeg was not found. It should sit in the same folder as this program; unzip the whole download again, then restart.';
  $('summary').textContent = text;
  const start = $('start');
  start.textContent = state.tab === 'compress' ? 'Compress' : 'Decompress';
  start.disabled = state.running || state.estimating || !!state.scanning || !list.some((it) => !it.job || it.job.status !== 'ok') || (t && !t.FFmpeg);
  start.hidden = state.running;
  $('cancel').hidden = !(state.running || state.estimating);
  // Finished clips are not processed again until their status is reset, for example to try other settings.
  const finished = (it) => it.job && it.job.status !== 'running' && it.job.status !== 'waiting';
  const picked = state.tab === 'compress' && items().find((it) => it.path === state.selected);
  $('resetSel').hidden = state.running || !(picked && finished(picked));
  $('resetAll').hidden = state.running || !items().some(finished);
  $('checkAll').checked = state.settings.checkAll;
  $('checkAll').disabled = state.running;
  const canLUT = !t || t.LUT;
  if (!canLUT) state.settings.lut = false;
  $('lut').checked = state.settings.lut;
  $('lut').disabled = state.running || !canLUT;
  $('lutHint').textContent = canLUT
    ? 'A .cube file, one per clip, that makes the archive look like the clip does in Blackmagic RAW with its as-shot settings, so it can be viewed or edited without decompressing. It cannot recover highlights, and colours the clip never shows may be off.'
    : 'Not available: ' + t.LUTReason;
  $('quality').disabled = state.running || !state.settings.quality;
  $('qualityAuto').disabled = state.running;
  // The list is not changed by hand while files are still being read into it.
  for (const id of ['addFiles', 'addFolder', 'clear']) $(id).disabled = state.running || !!state.scanning;
  for (const r of document.querySelectorAll('input[type=radio]')) r.disabled = state.running;
  for (const b of $('speed').children) b.disabled = state.running;
}

function render() { renderList(); renderSettings(); renderFooter(); }

// ---------- actions ----------
// Files dropped while others are still being read wait here, with the tab they were dropped on.
const waiting = [];

async function addPaths(paths, tab = state.tab) {
  if (!paths || !paths.length) return;
  if (state.scanning) { waiting.push({ paths, tab }); return; }
  state.scanning = { tab, done: 0, total: -1 };
  render();
  try {
    const found = await api.Scan(paths, tab === 'compress' ? 'clip' : 'archive');
    const have = new Set(state.lists[tab].map((it) => it.path));
    for (const it of found || []) if (!have.has(it.path)) state.lists[tab].push(it);
    if (tab === 'compress' && !state.selected && state.lists.compress.length) state.selected = state.lists.compress[0].path;
  } finally {
    state.scanning = null;
    const next = waiting.shift();
    if (next) addPaths(next.paths, next.tab);
    else render();
  }
}

$('addFiles').addEventListener('click', async () => addPaths(await api.PickFiles(state.tab === 'compress' ? 'clip' : 'archive')));
$('addFolder').addEventListener('click', async () => {
  const dir = await api.PickFolder('Add every ' + (state.tab === 'compress' ? 'clip' : 'archive') + ' in a folder', '');
  if (dir) addPaths([dir]);
});
$('clear').addEventListener('click', () => { state.lists[state.tab] = []; if (state.tab === 'compress') { state.selected = null; state.estimate = null; } render(); });

for (const b of document.querySelectorAll('nav button')) {
  b.addEventListener('click', () => { if (!state.running) { state.tab = b.dataset.tab; state.notice = ''; render(); } });
}
for (const b of $('output').children) {
  b.addEventListener('click', () => { state.settings.output = b.dataset.value; render(); });
}
for (const [group, key] of [['encoder', 'encoder'], ['speed', 'speed']]) {
  for (const b of $(group).children) {
    b.addEventListener('click', () => {
      if (state.settings[key] !== b.dataset.value) { state.settings[key] = b.dataset.value; state.estimate = null; }
      render();
    });
  }
}
for (const b of $('layers').children) {
  b.addEventListener('click', () => {
    const v = Number(b.dataset.value);
    if (state.settings.layers !== v) { state.settings.layers = v; state.estimate = null; }
    render();
  });
}
for (const r of document.querySelectorAll('input[name=mode]')) {
  r.addEventListener('change', () => { state.settings.intra = r.value === 'intra' && r.checked; state.estimate = null; render(); });
}
for (const r of document.querySelectorAll('input[name=rebuild]')) {
  r.addEventListener('change', () => { if (r.checked) state.rebuild = r.value; render(); });
}
$('quality').addEventListener('input', (e) => { state.settings.quality = state.manualQuality = Number(e.target.value); render(); });
$('qualityAuto').addEventListener('change', (e) => { state.settings.quality = e.target.checked ? 0 : state.manualQuality; render(); });

$('estimate').addEventListener('click', async () => {
  const sel = state.lists.compress.find((it) => it.path === state.selected);
  if (!sel) return;
  state.estimating = true;
  state.estimateFraction = 0;
  state.estimate = null;
  render();
  try {
    const kind = state.settings.output;
    const res = kind === 'braw' ? await api.ShrinkEstimate(sel.path) : await api.Estimate(sel.path, settings());
    state.estimate = Object.assign(res, { name: sel.name, intra: state.settings.intra, kind });
  } catch (err) {
    if (String(err).indexOf('canceled') < 0) $('estimateHint').textContent = 'Could not estimate: ' + err;
  }
  state.estimating = false;
  render();
});

const settings = (outDir, replace) => Object.assign({}, state.settings, { outDir: outDir || '', replace: !!replace });

// askExisting asks what to do with results that already exist: 'replace', 'skip' or 'cancel'.
function askExisting(existing, total, outDir) {
  const names = existing.map((p) => p.split(/[\\/]/).pop());
  const shown = names.slice(0, 6).join('\n') + (names.length > 6 ? '\n… and ' + (names.length - 6) + ' more' : '');
  $('askTitle').textContent = existing.length === 1 && total === 1
    ? 'This file already exists in the chosen folder'
    : existing.length + ' of ' + total + ' files already exist in the chosen folder';
  $('askText').textContent = outDir + '\n\nResults already there for:\n' + shown +
    '\n\nReplace makes them again; each old file is only replaced once its new version is finished and checked.';
  const dlg = $('ask');
  return new Promise((resolve) => {
    let answer = 'cancel';
    for (const b of dlg.querySelectorAll('button')) b.onclick = () => { answer = b.dataset.answer; dlg.close(); };
    dlg.onclose = () => resolve(answer);
    dlg.showModal();
  });
}

$('start').addEventListener('click', async () => {
  const todo = items().filter((it) => !it.error && !(it.job && it.job.status === 'ok'));
  if (!todo.length) return;
  // Where to save is asked every time; nothing is written until a folder has been chosen.
  const tab = state.tab;
  const what = tab === 'decompress' ? 'rebuilt clips' : state.settings.output === 'braw' ? 'smaller clips' : 'archives';
  let outDir = '';
  try { outDir = await api.PickFolder('Save the ' + what + ' to', lastDir(tab)); } catch (err) { outDir = ''; }
  if (!outDir || state.running || state.tab !== tab) return;
  // Smaller clips keep their names, so they cannot be saved where the originals are.
  state.notice = '';
  if (tab === 'compress' && state.settings.output === 'braw') {
    const same = (a, b) => a.replace(/[\\/]+$/, '').toLowerCase() === b.replace(/[\\/]+$/, '').toLowerCase();
    const clash = todo.filter((it) => same(it.folder || '', outDir));
    if (clash.length) {
      state.notice = 'Choose another folder: smaller clips keep their names, and ' +
        (clash.length === todo.length ? 'that folder holds the originals.' : 'that folder holds ' + clash.length + ' of the originals.');
      render();
      return;
    }
  }
  rememberDir(tab, outDir);
  // Results that already exist are never replaced without asking.
  const kind = tab === 'decompress' ? 'clip' : state.settings.output === 'braw' ? 'braw' : 'archive';
  let replace = false;
  try {
    const existing = await api.Existing(todo.map((it) => it.path), outDir, kind) || [];
    if (existing.length) {
      const answer = await askExisting(existing, todo.length, outDir);
      if (answer === 'cancel' || state.running || state.tab !== tab) return;
      replace = answer === 'replace';
    }
  } catch (err) { replace = false; }
  for (const it of todo) it.job = { status: 'waiting' };
  state.running = true;
  render();
  try {
    const paths = todo.map((it) => it.path);
    if (tab === 'compress') await api.Compress(paths, settings(outDir, replace));
    else await api.Decompress(paths, outDir, state.rebuild === 'source', replace);
  } catch (err) {
    for (const it of todo) it.job = { status: 'failed', message: String(err) };
    state.running = false;
    render();
  }
});
$('cancel').addEventListener('click', () => api.Cancel());
$('resetSel').addEventListener('click', () => {
  const it = items().find((o) => o.path === state.selected);
  if (it) delete it.job;
  render();
});
$('resetAll').addEventListener('click', () => { for (const it of items()) delete it.job; render(); });
$('checkAll').addEventListener('change', (e) => { state.settings.checkAll = e.target.checked; render(); });
$('lut').addEventListener('change', (e) => { state.settings.lut = e.target.checked; render(); });

// Files and folders dropped on the window are added to the current list.
if (inApp) window.runtime.OnFileDrop((x, y, paths) => { if (!state.running) addPaths(paths); }, false);

const findItem = (path) => state.lists.compress.concat(state.lists.decompress).find((it) => it.path === path && it.job);

on('progress', (p) => {
  if (p.stage === 'estimate') {
    state.estimateFraction = p.fraction;
    renderEstimateProgress();
    return;
  }
  const it = findItem(p.path);
  if (!it) return;
  it.job = { status: 'running', stage: p.stage, fraction: p.fraction };
  renderList();
});
on('done', (d) => {
  const it = findItem(d.path);
  if (it) it.job = { status: d.status, message: d.message, output: d.output, size: d.size, seconds: d.seconds };
  renderList();
});
on('scan', (s) => {
  if (!state.scanning) return;
  state.scanning.done = s.done;
  state.scanning.total = s.total;
  renderList();
});
on('idle', () => { state.running = false; render(); });

api.ShrinkRatios().then((r) => { state.ratios = r || []; render(); });
api.Tools().then((t) => {
  state.tools = t;
  const box = $('tools');
  if (!t.FFmpeg) { box.textContent = 'ffmpeg not found'; box.classList.add('bad'); }
  else box.textContent = 'ffmpeg ' + t.Version + (t.NVENC ? ' · NVIDIA GPU ready' : ' · no NVIDIA GPU, using CPU');
  if (!t.NVENC && t.X265) state.settings.encoder = 'x265';
  render();
});
render();

// ---------- stand-in backend for layout work in a browser ----------
var mockEvents;
function mockApi() {
  const handlers = {};
  mockEvents = { on: (n, f) => { handlers[n] = f; }, emit: (n, d) => handlers[n] && handlers[n](d) };
  const GB = 2 ** 30, MB = 2 ** 20;
  const clips = [
    { name: 'A250_10102147_C176.braw', size: 2.19 * GB, width: 2688, height: 1512, fps: 120, ratio: 5.9, frames: 2224 },
    { name: 'A250_10101839_C001.braw', size: 1.14 * GB, width: 4096, height: 2160, fps: 60, ratio: 10.1, frames: 1400 },
    { name: 'A250_10101839_C002.braw', size: 935 * MB, width: 4096, height: 2160, fps: 60, ratio: 9.4, frames: 1100 },
  ];
  const archives = [
    { name: 'A250_10102147_C176.braw.mkv', size: 720 * MB, width: 2720, height: 1528, fps: 120, sourceSize: 2.19 * GB, autoSize: 1.49 * GB, sourceQSize: 1.68 * GB },
    { name: 'Sideline_001.braw.mkv', size: 622 * MB, width: 2912, height: 1528, fps: 120, sourceSize: 2.74 * GB, autoSize: 2.81 * GB, sourceQSize: 2.81 * GB },
  ];
  const wrap = (a) => a.map((c) => Object.assign({ path: 'D:\\Footage\\' + c.name, folder: 'D:\\Footage' }, c));
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  let stop = false;
  async function fake(paths, stages) {
    stop = false;
    for (const path of paths) {
      if (stop) { mockEvents.emit('done', { path, status: 'canceled', message: 'Canceled' }); continue; }
      for (let k = 0; k < stages.length; k++) for (let f = 0; f <= 1.001 && !stop; f += 0.1) { mockEvents.emit('progress', { path, stage: stages[k], fraction: (k + f) / stages.length }); await sleep(80); }
      mockEvents.emit('done', stop ? { path, status: 'canceled', message: 'Canceled' }
        : { path, status: 'ok', message: stages.length > 1 ? '22.1% of the original size, 96.8% of detail kept, every frame checked' : 'Rebuilt', output: path, size: 480 * MB, seconds: 187 });
    }
    mockEvents.emit('idle');
  }
  return {
    Tools: async () => ({ FFmpeg: 'ffmpeg', Version: '7.1', NVENC: true, X265: true, LUT: true, LUTReason: '' }),
    PickFiles: async (kind) => wrap(kind === 'clip' ? clips : archives).map((c) => c.path),
    PickFolder: async () => 'D:\\Archive',
    Scan: async (paths, kind) => {
      const found = wrap(kind === 'clip' ? clips : archives).filter((c) => paths.includes(c.path) || paths.length === 1);
      mockEvents.emit('scan', { done: 0, total: -1 });
      await sleep(600);
      for (let n = 0; n <= found.length; n++) { mockEvents.emit('scan', { done: n, total: found.length }); await sleep(500); }
      return found;
    },
    Estimate: async (path, s) => {
      for (let f = 0; f <= 1.001; f += 0.05) { mockEvents.emit('progress', { path, stage: 'estimate', fraction: f }); await sleep(80); }
      const base = s.intra ? [59, 52, 42, 32, 25, 23] : [15.8, 13.2, 11.1, 7.7, 5.7, 4.6];
      const kept = s.intra ? [99.7, 99.5, 99.2, 98.9, 98.4, 97.9] : [95.9, 96.2, 96.6, 96.8, 96.1, 95.5];
      return { path, sourceBytes: 2.19 * GB, sourceRatio: 5.9, sampled: 96, frames: 2224, autoQuality: 10, sourceStep: 4.23,
        layers: s.encoder === 'x265' ? 1 : s.layers || 1, layersAuto: !s.layers,
        rows: [4, 6, 8, 10, 12, 14].map((q, i) => ({ quality: q, archiveBytes: 2.19 * GB * base[i] / 100, archiveRatio: 590 / base[i],
          detailKept: kept[i], rebuiltBytes: (s.intra ? 2.14 - i * 0.15 : 1.2 - i * 0.06) * GB, rebuiltRatio: s.intra ? 6.1 + i * 0.6 : 10.8 + i * 0.8 })) };
    },
    Existing: async (paths) => window.mockExisting ? paths.slice(0, window.mockExisting) : [],
    ShrinkRatios: async () => [5, 8, 10, 12, 15, 18, 20, 22, 25],
    ShrinkEstimate: async (path) => {
      for (let f = 0; f <= 1.001; f += 0.1) { mockEvents.emit('progress', { path, stage: 'estimate', fraction: f }); await sleep(60); }
      const kept = [99.5, 99.4, 99.1, 98.5, 98.0, 97.7, 96.9, 95.4];
      return { path, sourceBytes: 2.19 * GB, sourceRatio: 5.9, maxRatio: 27,
        rows: [8, 10, 12, 15, 18, 20, 22, 25].map((r, i) => ({ ratio: r, bytes: 2.19 * GB * 5.9 / r, detailKept: kept[i] })) };
    },
    Compress: async (paths, s) => { fake(paths, s.output === 'braw' ? ['analyse', 'encode', 'finish'] : ['analyse', 'encode', 'verify', 'finish']); },
    Decompress: async (paths) => { fake(paths, ['rebuild']); },
    Cancel: async () => { stop = true; },
    Reveal: async () => {},
  };
}
