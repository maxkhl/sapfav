'use strict';

// ---------- Hilfsfunktionen ----------

const $ = (sel, root = document) => root.querySelector(sel);

function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (v === true) e.setAttribute(k, '');
    else if (v !== false && v != null) e.setAttribute(k, v);
  }
  e.append(...children.filter(c => c != null));
  return e;
}

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (method !== 'GET') {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body ?? {});
  }
  return handle(await fetch(path, opts));
}

async function upload(path, formData) {
  return handle(await fetch(path, {
    method: 'POST',
    headers: { 'X-Requested-With': 'XMLHttpRequest' },
    body: formData,
  }));
}

async function handle(res) {
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try { msg = (await res.json()).error || msg; } catch { /* kein JSON */ }
    throw new Error(msg);
  }
  return res.status === 204 ? null : res.json();
}

let toastTimer;
function toast(msg, isError = false) {
  const t = $('#toast');
  t.textContent = msg;
  t.classList.toggle('error', isError);
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, isError ? 5000 : 1800);
}

function showError(form, err) {
  const p = $('.error', form);
  p.textContent = err ? err.message || String(err) : '';
  p.hidden = !err;
}

// ---------- Zustand ----------

const EXPANDED_KEY = 'sapfav.expanded';

const state = {
  tree: [],
  byId: new Map(),
  expanded: new Set(),
  query: '',
};

try {
  state.expanded = new Set(JSON.parse(localStorage.getItem(EXPANDED_KEY) || '[]'));
} catch { /* ohne gespeicherten Zustand weiter */ }

function saveExpanded() {
  try { localStorage.setItem(EXPANDED_KEY, JSON.stringify([...state.expanded])); } catch { /* egal */ }
}

async function reload() {
  try {
    state.tree = await api('GET', '/api/tree');
  } catch (err) {
    toast('Laden fehlgeschlagen: ' + err.message, true);
    return;
  }
  indexTree();
  render();
}

// indexTree ergänzt Verweise, die die Oberfläche braucht (Parent, Position, Anzahl).
function indexTree() {
  state.byId.clear();
  const walk = (nodes, parent) => {
    let tx = 0;
    nodes.forEach((n, i) => {
      n.children = n.children || [];
      n.parent = parent;
      n.index = i;
      n.siblings = nodes.length;
      state.byId.set(n.id, n);
      n.txCount = n.kind === 'folder' ? walk(n.children, n) : 0;
      tx += n.kind === 'folder' ? n.txCount : 1;
    });
    return tx;
  };
  walk(state.tree, null);
}

// ---------- Suche ----------

function searchTerms() {
  return state.query.toLowerCase().split(/\s+/).filter(Boolean);
}

function matches(n, terms) {
  const text = [n.tcode || '', n.title, n.description || ''].join('\n').toLowerCase();
  return terms.every(t => text.includes(t));
}

// filterTree liefert die sichtbaren Knoten. Ohne Suchbegriffe gilt der
// Aufklappzustand; mit Suche sind Ordner mit Treffern offen, leere ausgeblendet.
// Passt ein Ordner selbst, wird sein ganzer Inhalt gezeigt.
function filterTree(nodes, terms) {
  const out = [];
  for (const n of nodes) {
    if (!terms.length) {
      out.push({ n, open: state.expanded.has(n.id), kids: n.kind === 'folder' ? filterTree(n.children, terms) : [] });
    } else if (n.kind === 'tx') {
      if (matches(n, terms)) out.push({ n, kids: [] });
    } else if (matches(n, terms)) {
      out.push({ n, open: true, kids: filterTree(n.children, []) });
    } else {
      const kids = filterTree(n.children, terms);
      if (kids.length) out.push({ n, open: true, kids });
    }
  }
  return out;
}

function highlight(text, terms) {
  const frag = document.createDocumentFragment();
  if (!text) return frag;
  const lower = text.toLowerCase();
  const ranges = [];
  for (const t of terms) {
    for (let i = lower.indexOf(t); i !== -1; i = lower.indexOf(t, i + t.length)) {
      ranges.push([i, i + t.length]);
    }
  }
  ranges.sort((a, b) => a[0] - b[0]);
  let pos = 0;
  for (const [start, end] of ranges) {
    if (end <= pos) continue;
    const s = Math.max(start, pos);
    if (s > pos) frag.append(text.slice(pos, s));
    frag.append(el('mark', {}, text.slice(s, end)));
    pos = end;
  }
  if (pos < text.length) frag.append(text.slice(pos));
  return frag;
}

// ---------- Darstellung ----------

function render() {
  const terms = searchTerms();
  const items = filterTree(state.tree, terms);

  // Fokus über das Neuzeichnen retten (z. B. nach Hoch/Runter).
  const focusKey = document.activeElement?.dataset?.focus;

  $('#tree').replaceChildren(renderList(items, terms));

  const total = state.byId.size;
  const folders = [...state.byId.values()].filter(n => n.kind === 'folder').length;
  let hits = 0;
  if (terms.length) {
    for (const n of state.byId.values()) if (matches(n, terms)) hits++;
  }
  $('#stats').textContent = terms.length
    ? `${hits} Treffer`
    : total ? `${folders} Ordner · ${total - folders} Transaktionen` : '';
  $('#empty').hidden = total > 0;
  $('#nohits').hidden = !(total && terms.length && !items.length);

  if (focusKey) $(`[data-focus="${CSS.escape(focusKey)}"]`)?.focus();
}

function renderList(items, terms) {
  const ul = el('ul');
  for (const item of items) ul.append(renderNode(item, terms));
  return ul;
}

function renderNode({ n, open, kids }, terms) {
  const li = el('li', { class: n.kind });
  const searching = terms.length > 0;
  let main;

  if (n.kind === 'folder') {
    main = el('button', {
      type: 'button', class: 'main', 'aria-expanded': String(!!open), 'data-focus': `${n.id}:main`,
      title: searching ? null : (open ? 'Zuklappen' : 'Aufklappen'),
      onclick: () => {
        if (searching) return;
        if (state.expanded.has(n.id)) state.expanded.delete(n.id); else state.expanded.add(n.id);
        saveExpanded();
        render();
      },
    },
      el('span', { class: 'chevron', 'aria-hidden': 'true' }, open ? '▾' : '▸'),
      el('span', { class: 'title' }, highlight(n.title, terms)),
      el('span', { class: 'count' }, String(n.txCount)),
    );
  } else {
    main = el('button', {
      type: 'button', class: 'main', title: 'T-Code kopieren', 'data-focus': `${n.id}:main`,
      onclick: () => copyText(n.tcode),
    },
      el('span', { class: 'tcode' }, highlight(n.tcode, terms)),
      el('span', { class: 'title' }, highlight(n.title, terms)),
    );
  }

  li.append(el('div', { class: 'row' }, main, renderActions(n)));
  if (n.description) li.append(el('div', { class: 'description' }, highlight(n.description, terms)));

  if (n.kind === 'folder' && open) {
    if (kids.length) li.append(renderList(kids, terms));
    else if (!searching) li.append(el('div', { class: 'empty-folder' }, 'leer'));
  }
  return li;
}

function renderActions(n) {
  const btn = (label, title, action, onclick, disabled = false) =>
    el('button', { type: 'button', title, 'aria-label': title, 'data-focus': `${n.id}:${action}`, disabled, onclick }, label);
  const name = n.kind === 'tx' ? n.tcode : n.title;
  return el('div', { class: 'actions' },
    n.kind === 'folder' ? btn('+', `In „${name}“ anlegen`, 'add', () => openEdit({ kind: 'tx', parentId: n.id })) : null,
    btn('↑', 'Nach oben', 'up', () => reorder(n, 'up'), n.index === 0),
    btn('↓', 'Nach unten', 'down', () => reorder(n, 'down'), n.index === n.siblings - 1),
    btn('⇥', 'Verschieben nach …', 'move', () => openMove(n)),
    btn('✎', 'Bearbeiten', 'edit', () => openEdit({ node: n })),
    btn('✕', 'Löschen', 'delete', () => openDelete(n)),
  );
}

// ---------- Kopieren ----------

async function copyText(text) {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
    } else {
      legacyCopy(text);
    }
    toast(`„${text}“ kopiert`);
  } catch {
    try {
      legacyCopy(text);
      toast(`„${text}“ kopiert`);
    } catch {
      toast('Kopieren nicht möglich', true);
    }
  }
}

// legacyCopy funktioniert auch ohne HTTPS (z. B. direkt per IP im LAN).
function legacyCopy(text) {
  const ta = el('textarea', { readonly: true, style: 'position:fixed;top:0;left:0;opacity:0' });
  ta.value = text;
  document.body.append(ta);
  ta.select();
  const ok = document.execCommand('copy');
  ta.remove();
  if (!ok) throw new Error('copy failed');
}

// ---------- Ordnerauswahl ----------

// fillFolderSelect listet alle Ordner eingerückt; exclude blendet einen Ordner
// samt Unterordnern aus (man kann nichts in sich selbst verschieben).
function fillFolderSelect(select, { exclude = null, selected = null } = {}) {
  select.replaceChildren(el('option', { value: '' }, 'Oberste Ebene'));
  const walk = (nodes, depth) => {
    for (const n of nodes) {
      if (n.kind !== 'folder' || n.id === exclude) continue;
      select.append(el('option', { value: n.id, selected: n.id === selected }, '   '.repeat(depth) + n.title));
      walk(n.children, depth + 1);
    }
  };
  walk(state.tree, 1);
}

const selectedParent = select => (select.value === '' ? null : Number(select.value));

// ---------- Anlegen / Bearbeiten ----------

let editing = null; // { node } beim Bearbeiten, sonst null

function openEdit({ node = null, kind = 'tx', parentId = null }) {
  const form = $('#edit-form');
  editing = node;
  form.reset();
  showError(form, null);

  $('#edit-heading').textContent = node
    ? (node.kind === 'tx' ? 'Transaktion bearbeiten' : 'Ordner bearbeiten')
    : 'Neu anlegen';
  $('#edit-kind').hidden = !!node;
  $('#edit-parent-label').hidden = !!node;

  if (node) {
    form.elements.tcode.value = node.tcode || '';
    form.elements.title.value = node.title;
    form.elements.description.value = node.description || '';
    kind = node.kind;
  } else {
    fillFolderSelect(form.elements.parent, { selected: parentId });
  }
  form.elements.kind.value = kind;
  updateEditKind();
  $('#edit-dialog').showModal();
  (kind === 'tx' ? form.elements.tcode : form.elements.title).focus();
}

function updateEditKind() {
  const form = $('#edit-form');
  const isTx = form.elements.kind.value === 'tx';
  $('#edit-tcode-label').hidden = !isTx;
  form.elements.tcode.required = isTx;
  form.elements.title.required = !isTx;
  $('#edit-title-caption').textContent = isTx ? 'Kurztext' : 'Ordnername';
}

$('#edit-kind').addEventListener('change', updateEditKind);

$('#edit-form').addEventListener('submit', async e => {
  if (e.submitter?.value !== 'ok') return;
  e.preventDefault();
  const form = e.target;
  const body = {
    tcode: form.elements.kind.value === 'tx' ? form.elements.tcode.value : null,
    title: form.elements.title.value,
    description: form.elements.description.value,
  };
  try {
    let saved;
    if (editing) {
      saved = await api('PUT', `/api/nodes/${editing.id}`, body);
    } else {
      body.kind = form.elements.kind.value;
      body.parent_id = selectedParent(form.elements.parent);
      saved = await api('POST', '/api/nodes', body);
      if (body.parent_id != null) {
        state.expanded.add(body.parent_id);
        saveExpanded();
      }
    }
    $('#edit-dialog').close();
    await reload();
    $(`[data-focus="${saved.id}:main"]`)?.focus();
  } catch (err) {
    showError(form, err);
  }
});

// ---------- Verschieben / Reihenfolge ----------

let moving = null;

function openMove(n) {
  const form = $('#move-form');
  moving = n;
  showError(form, null);
  $('#move-text').textContent = `„${n.kind === 'tx' ? n.tcode + ' – ' : ''}${n.title}“`;
  fillFolderSelect(form.elements.parent, { exclude: n.kind === 'folder' ? n.id : null, selected: n.parent?.id ?? null });
  $('#move-dialog').showModal();
  form.elements.parent.focus();
}

$('#move-form').addEventListener('submit', async e => {
  if (e.submitter?.value !== 'ok') return;
  e.preventDefault();
  const form = e.target;
  const parentId = selectedParent(form.elements.parent);
  try {
    await api('POST', `/api/nodes/${moving.id}/move`, { parent_id: parentId });
    if (parentId != null) {
      state.expanded.add(parentId);
      saveExpanded();
    }
    $('#move-dialog').close();
    await reload();
  } catch (err) {
    showError(form, err);
  }
});

async function reorder(n, direction) {
  try {
    await api('POST', `/api/nodes/${n.id}/reorder`, { direction });
    await reload();
  } catch (err) {
    toast(err.message, true);
  }
}

// ---------- Löschen ----------

function openDelete(n) {
  const form = $('#delete-form');
  showError(form, null);
  const label = n.kind === 'tx' ? `Transaktion „${n.tcode} – ${n.title}“` : `Ordner „${n.title}“`;
  const buttons = [el('button', { value: 'cancel', formnovalidate: true }, 'Abbrechen')];

  const run = children => async () => {
    try {
      await api('DELETE', `/api/nodes/${n.id}`, children ? { children } : {});
      $('#delete-dialog').close();
      await reload();
    } catch (err) {
      showError(form, err);
    }
  };

  if (n.kind === 'folder' && n.children.length) {
    const count = n.children.length;
    $('#delete-text').textContent =
      `${label} enthält ${count} ${count === 1 ? 'Eintrag' : 'Einträge'} direkt darunter. Was soll mit dem Inhalt passieren?`;
    buttons.push(
      el('button', { type: 'button', onclick: run('lift') }, 'Inhalt eine Ebene hoch'),
      el('button', { type: 'button', class: 'danger', onclick: run('delete') }, 'Mit Inhalt löschen'),
    );
  } else {
    $('#delete-text').textContent = `${label} löschen?`;
    buttons.push(el('button', { type: 'button', class: 'primary', onclick: run(null) }, 'Löschen'));
  }
  $('#delete-buttons').replaceChildren(...buttons);
  $('#delete-dialog').showModal();
  buttons[0].focus();
}

// ---------- Import ----------

const importForm = $('#import-form');

function resetImportPreview() {
  $('#import-preview').hidden = true;
  showError(importForm, null);
}

$('#open-import').addEventListener('click', () => {
  importForm.reset();
  resetImportPreview();
  $('#import-dialog').showModal();
});

importForm.elements.file.addEventListener('change', resetImportPreview);
importForm.elements.encoding.addEventListener('change', resetImportPreview);

function fillTable(details, rows, summary) {
  details.hidden = rows.length === 0;
  details.open = false;
  $('summary', details).textContent = summary;
  $('tbody', details).replaceChildren(...rows.map(cells =>
    el('tr', {}, ...cells.map(([text, cls]) => el('td', { class: cls }, String(text))))));
}

importForm.addEventListener('submit', async e => {
  const action = e.submitter?.value;
  if (action !== 'preview' && action !== 'import') return;
  e.preventDefault();

  const data = new FormData(importForm);
  const buttonsBusy = busy => importForm.querySelectorAll('button').forEach(b => { b.disabled = busy; });
  showError(importForm, null);
  buttonsBusy(true);
  try {
    if (action === 'preview') {
      data.delete('mode');
      const p = await upload('/api/import/preview', data);
      $('#import-summary').textContent =
        `${p.folders} Ordner und ${p.transactions} Transaktionen erkannt` +
        (p.fallbacks ? `, davon ${p.fallbacks} Zeilen mit verrutschten Spalten per Fallback zerlegt.` : '.');
      fillTable($('#import-unparsable'),
        p.unparsable.map(u => [[u.line], [u.reason], [u.text, 'raw']]),
        `${p.unparsable.length} Zeilen nicht übernommen`);
      fillTable($('#import-orphans'),
        p.orphans.map(o => [[o.line], [o.id], [o.parent_id], [o.reason]]),
        `${p.orphans.length} Einträge mit Parent-ID ins Leere`);
      $('#import-existing').textContent = p.existing
        ? `– löscht ${p.existing} bestehende Einträge`
        : '';
      $('#import-preview').hidden = false;
    } else {
      const mode = data.get('mode');
      if (mode === 'replace' && !confirm('Bestand wirklich ersetzen? Alle bisherigen Einträge samt Beschreibungen werden gelöscht.')) {
        return;
      }
      const s = await upload('/api/import', data);
      $('#import-dialog').close();
      await reload();
      toast(`Importiert: ${s.folders} Ordner, ${s.transactions} Transaktionen`);
    }
  } catch (err) {
    showError(importForm, err);
  } finally {
    buttonsBusy(false);
  }
});

// ---------- Kopfzeile, Tastatur ----------

const search = $('#search');

search.addEventListener('input', () => {
  state.query = search.value;
  render();
});

$('#new-tx').addEventListener('click', () => openEdit({ kind: 'tx' }));
$('#new-folder').addEventListener('click', () => openEdit({ kind: 'folder' }));

document.addEventListener('keydown', e => {
  if (document.querySelector('dialog[open]')) return;
  const inField = e.target.closest?.('input, textarea, select, [contenteditable]');

  if (e.key === '/' && !inField && !e.ctrlKey && !e.metaKey && !e.altKey) {
    e.preventDefault();
    search.focus();
    search.select();
  } else if (e.key === 'Escape' && (state.query || e.target === search)) {
    e.preventDefault();
    search.value = '';
    state.query = '';
    render();
  }
});

reload();
