'use strict';

// ---------- Hilfsfunktionen ----------

const $ = (sel, root = document) => root.querySelector(sel);

function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v === true ? '' : v);
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

function queryString(params) {
  const q = new URLSearchParams(Object.entries(params).filter(([, v]) => v != null && v !== ''));
  const s = q.toString();
  return s ? '?' + s : '';
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

function fillTable(details, rows, summary) {
  details.hidden = rows.length === 0;
  details.open = false;
  $('summary', details).textContent = summary;
  $('tbody', details).replaceChildren(...rows.map(cells =>
    el('tr', {}, ...cells.map(([text, cls]) => el('td', { class: cls }, String(text))))));
}

// ---------- Zustand ----------

const EXPANDED_KEY = 'sapfav.expanded';
const VIEW_KEY = 'sapfav.view';

function loadSetting(key, fallback) {
  try {
    const v = localStorage.getItem(key);
    return v == null ? fallback : JSON.parse(v);
  } catch {
    return fallback;
  }
}

function saveSetting(key, value) {
  try { localStorage.setItem(key, JSON.stringify(value)); } catch { /* ohne Speicher weiter */ }
}

const state = {
  tree: [],            // oberste Ebene aller Bereiche, wie von /api/tree
  customers: [],
  byId: new Map(),
  roots: new Map(),    // Bereich ('g' oder Kunden-ID) -> oberste Ebene
  counts: new Map(),   // Bereich -> { folders, tx }
  expanded: new Set(loadSetting(EXPANDED_KEY, [])),
  view: String(loadSetting(VIEW_KEY, 'all')), // 'all' | 'general' | Kunden-ID
  query: '',
};

const saveExpanded = () => saveSetting(EXPANDED_KEY, [...state.expanded]);
const scopeKey = customerId => (customerId == null ? 'g' : String(customerId));
const customerById = id => state.customers.find(c => String(c.id) === String(id));

function setView(view, rerender = true) {
  state.view = String(view);
  saveSetting(VIEW_KEY, state.view);
  $('#view').value = state.view;
  if (rerender) render();
}

async function reload() {
  try {
    [state.tree, state.customers] = await Promise.all([api('GET', '/api/tree'), api('GET', '/api/customers')]);
  } catch (err) {
    toast('Laden fehlgeschlagen: ' + err.message, true);
    return;
  }
  if (state.view !== 'all' && state.view !== 'general' && !customerById(state.view)) {
    state.view = 'all';
    saveSetting(VIEW_KEY, state.view);
  }
  indexTree();
  fillViewSelect();
  render();
  if ($('#customers-dialog').open) renderCustomerList();
}

// indexTree gruppiert nach Bereich und ergänzt Verweise für die Oberfläche.
function indexTree() {
  state.byId.clear();
  state.roots.clear();
  state.counts.clear();
  for (const n of state.tree) {
    const key = scopeKey(n.customer_id);
    if (!state.roots.has(key)) state.roots.set(key, []);
    state.roots.get(key).push(n);
  }
  const walk = (nodes, parent, count) => {
    let tx = 0;
    nodes.forEach((n, i) => {
      n.children = n.children || [];
      n.parent = parent;
      n.index = i;
      n.siblings = nodes.length;
      state.byId.set(n.id, n);
      if (n.kind === 'folder') {
        count.folders++;
        n.txCount = walk(n.children, n, count);
        tx += n.txCount;
      } else {
        count.tx++;
        tx++;
      }
    });
    return tx;
  };
  for (const [key, nodes] of state.roots) {
    const count = { folders: 0, tx: 0 };
    walk(nodes, null, count);
    state.counts.set(key, count);
  }
}

function allScopes() {
  return [
    { key: 'g', customerId: null, name: 'Allgemein', notes: null },
    ...state.customers.map(c => ({ key: String(c.id), customerId: c.id, name: c.name, notes: c.notes })),
  ];
}

function visibleScopes() {
  const scopes = allScopes();
  if (state.view === 'all') return scopes;
  if (state.view === 'general') return scopes.slice(0, 1);
  return scopes.filter(s => s.key === 'g' || s.key === state.view);
}

function fillViewSelect() {
  $('#view').replaceChildren(
    el('option', { value: 'all' }, 'Alle Kunden'),
    el('option', { value: 'general' }, 'Nur Allgemein'),
    ...state.customers.map(c => el('option', { value: String(c.id) }, c.name)),
  );
  $('#view').value = state.view;
}

// ---------- Suche ----------

function searchTerms() {
  return state.query.toLowerCase().split(/\s+/).filter(Boolean);
}

function matches(n, terms) {
  const text = [n.tcode || '', n.title, n.description || ''].join('\n').toLowerCase();
  return terms.every(t => text.includes(t));
}

function countMatches(nodes, terms) {
  let c = 0;
  for (const n of nodes) {
    if (matches(n, terms)) c++;
    c += countMatches(n.children, terms);
  }
  return c;
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
  // Fokus über das Neuzeichnen retten (z. B. nach Hoch/Runter).
  const focusKey = document.activeElement?.dataset?.focus;

  const sections = [];
  let folders = 0;
  let tx = 0;
  let hits = 0;
  for (const scope of visibleScopes()) {
    const nodes = state.roots.get(scope.key) || [];
    const count = state.counts.get(scope.key) || { folders: 0, tx: 0 };
    const items = filterTree(nodes, terms);
    folders += count.folders;
    tx += count.tx;
    if (terms.length) {
      hits += countMatches(nodes, terms);
      if (!items.length) continue;
    }
    sections.push(renderScope(scope, count, items, terms));
  }

  $('#tree').replaceChildren(...sections);
  $('#stats').textContent = terms.length ? `${hits} Treffer` : `${folders} Ordner · ${tx} Transaktionen`;
  $('#nohits').hidden = !(terms.length && !sections.length);

  if (focusKey) $(`[data-focus="${CSS.escape(focusKey)}"]`)?.focus();
}

function renderScope(scope, count, items, terms) {
  const addTitle = `In „${scope.name}“ anlegen`;
  return el('section', { class: 'scope' },
    el('div', { class: 'scope-head' },
      el('h2', {}, scope.name),
      el('span', { class: 'count' }, `${count.tx} Transaktionen`),
      el('div', { class: 'actions' },
        el('button', {
          type: 'button', title: addTitle, 'aria-label': addTitle, 'data-focus': `scope-${scope.key}:add`,
          onclick: () => openEdit({ kind: 'tx', target: `top:${scope.key}` }),
        }, '+'),
      ),
    ),
    scope.notes ? el('p', { class: 'notes' }, scope.notes) : null,
    items.length ? renderList(items, terms) : el('p', { class: 'placeholder' }, 'Noch keine Favoriten.'),
  );
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
    n.kind === 'folder' ? btn('+', `In „${name}“ anlegen`, 'add', () => openEdit({ kind: 'tx', target: `f:${n.id}` })) : null,
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

// ---------- Zielauswahl (Bereich oder Ordner) ----------

// Werte: "top:g" / "top:<Kunden-ID>" = oberste Ebene eines Bereichs, "f:<id>" = Ordner.
// exclude blendet einen Ordner samt Unterordnern aus.
function fillTargetSelect(select, { exclude = null, selected = 'top:g' } = {}) {
  const groups = allScopes().map(scope => {
    const group = el('optgroup', { label: scope.name },
      el('option', { value: `top:${scope.key}` }, 'Oberste Ebene'));
    const walk = (nodes, depth) => {
      for (const n of nodes) {
        if (n.kind !== 'folder' || n.id === exclude) continue;
        group.append(el('option', { value: `f:${n.id}` }, '   '.repeat(depth) + n.title));
        walk(n.children, depth + 1);
      }
    };
    walk(state.roots.get(scope.key) || [], 1);
    return group;
  });
  select.replaceChildren(...groups);
  select.value = selected;
  if (select.selectedIndex < 0) select.value = 'top:g';
}

function parseTarget(value) {
  const [type, id] = value.split(':');
  if (type === 'f') return { parent_id: Number(id), customer_id: null };
  return { parent_id: null, customer_id: id === 'g' ? null : Number(id) };
}

const targetOf = n => (n.parent ? `f:${n.parent.id}` : `top:${scopeKey(n.customer_id)}`);
const defaultTarget = () => (customerById(state.view) ? `top:${state.view}` : 'top:g');

function expandTarget(target) {
  if (target.parent_id != null) {
    state.expanded.add(target.parent_id);
    saveExpanded();
  }
}

// ---------- Anlegen / Bearbeiten ----------

let editing = null; // Knoten beim Bearbeiten, sonst null

function openEdit({ node = null, kind = 'tx', target = defaultTarget() }) {
  const form = $('#edit-form');
  const f = form.elements;
  editing = node;
  form.reset();
  showError(form, null);

  $('#edit-heading').textContent = node
    ? (node.kind === 'tx' ? 'Transaktion bearbeiten' : 'Ordner bearbeiten')
    : 'Neu anlegen';
  $('#edit-kind').hidden = !!node;
  $('#edit-target-label').hidden = !!node;

  if (node) {
    f.tcode.value = node.tcode || '';
    f.title.value = node.title;
    f.description.value = node.description || '';
    kind = node.kind;
  } else {
    fillTargetSelect(f.target, { selected: target });
  }
  f.kind.value = kind;
  updateEditKind();
  $('#edit-dialog').showModal();
  (kind === 'tx' ? f.tcode : f.title).focus();
}

function updateEditKind() {
  const f = $('#edit-form').elements;
  const isTx = f.kind.value === 'tx';
  $('#edit-tcode-label').hidden = !isTx;
  f.tcode.required = isTx;
  f.title.required = !isTx;
  $('#edit-title-caption').textContent = isTx ? 'Kurztext' : 'Ordnername';
}

$('#edit-kind').addEventListener('change', updateEditKind);

$('#edit-form').addEventListener('submit', async e => {
  if (e.submitter?.value !== 'ok') return;
  e.preventDefault();
  const form = e.target;
  const f = form.elements;
  const body = {
    tcode: f.kind.value === 'tx' ? f.tcode.value : null,
    title: f.title.value,
    description: f.description.value,
  };
  try {
    let saved;
    if (editing) {
      saved = await api('PUT', `/api/nodes/${editing.id}`, body);
    } else {
      const target = parseTarget(f.target.value);
      saved = await api('POST', '/api/nodes', { ...body, kind: f.kind.value, ...target });
      expandTarget(target);
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
  fillTargetSelect(form.elements.target, { exclude: n.kind === 'folder' ? n.id : null, selected: targetOf(n) });
  $('#move-dialog').showModal();
  form.elements.target.focus();
}

$('#move-form').addEventListener('submit', async e => {
  if (e.submitter?.value !== 'ok') return;
  e.preventDefault();
  const form = e.target;
  const target = parseTarget(form.elements.target.value);
  try {
    await api('POST', `/api/nodes/${moving.id}/move`, target);
    expandTarget(target);
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

// ---------- Rückfragen ----------

// openChoice zeigt eine Rückfrage mit Abbrechen und den angegebenen Aktionen.
function openChoice({ title, text, choices }) {
  const form = $('#choice-form');
  showError(form, null);
  $('#choice-title').textContent = title;
  $('#choice-text').textContent = text;
  const cancel = el('button', { value: 'cancel', formnovalidate: true }, 'Abbrechen');
  const buttons = choices.map(c => el('button', {
    type: 'button',
    class: c.cls,
    onclick: async () => {
      try {
        await c.run();
        $('#choice-dialog').close();
      } catch (err) {
        showError(form, err);
      }
    },
  }, c.label));
  $('#choice-buttons').replaceChildren(cancel, ...buttons);
  $('#choice-dialog').showModal();
  cancel.focus();
}

function openDelete(n) {
  const label = n.kind === 'tx' ? `Transaktion „${n.tcode} – ${n.title}“` : `Ordner „${n.title}“`;
  const run = children => async () => {
    await api('DELETE', `/api/nodes/${n.id}`, children ? { children } : {});
    await reload();
  };
  if (n.kind === 'folder' && n.children.length) {
    const count = n.children.length;
    openChoice({
      title: 'Löschen',
      text: `${label} enthält ${count} ${count === 1 ? 'Eintrag' : 'Einträge'} direkt darunter. Was soll mit dem Inhalt passieren?`,
      choices: [
        { label: 'Inhalt eine Ebene hoch', run: run('lift') },
        { label: 'Mit Inhalt löschen', cls: 'danger', run: run('delete') },
      ],
    });
  } else {
    openChoice({ title: 'Löschen', text: `${label} löschen?`, choices: [{ label: 'Löschen', cls: 'primary', run: run(null) }] });
  }
}

// ---------- Kunden ----------

function renderCustomerList() {
  const list = $('#customer-list');
  if (!state.customers.length) {
    list.replaceChildren(el('li', { class: 'placeholder' }, 'Noch keine Kunden angelegt.'));
    return;
  }
  list.replaceChildren(...state.customers.map(c => el('li', {},
    el('div', { class: 'customer-main' },
      el('strong', {}, c.name),
      el('span', { class: 'count' }, `${c.node_count} ${c.node_count === 1 ? 'Eintrag' : 'Einträge'}`),
      c.notes ? el('div', { class: 'notes' }, c.notes) : null,
    ),
    el('div', { class: 'buttons' },
      el('button', { type: 'button', onclick: () => { setView(c.id); $('#customers-dialog').close(); } }, 'Anzeigen'),
      el('button', { type: 'button', onclick: () => openCustomerEdit(c) }, 'Bearbeiten'),
      el('button', { type: 'button', class: 'danger', onclick: () => openCustomerDelete(c) }, 'Löschen'),
    ),
  )));
}

$('#open-customers').addEventListener('click', () => {
  renderCustomerList();
  $('#customers-dialog').showModal();
});

$('#new-customer').addEventListener('click', () => openCustomerEdit(null));

let editingCustomer = null;

function openCustomerEdit(c) {
  const form = $('#customer-form');
  editingCustomer = c;
  form.reset();
  showError(form, null);
  $('#customer-heading').textContent = c ? 'Kunde bearbeiten' : 'Neuer Kunde';
  if (c) {
    form.elements.name.value = c.name;
    form.elements.notes.value = c.notes || '';
  }
  $('#customer-dialog').showModal();
  form.elements.name.focus();
}

$('#customer-form').addEventListener('submit', async e => {
  if (e.submitter?.value !== 'ok') return;
  e.preventDefault();
  const form = e.target;
  const body = { name: form.elements.name.value, notes: form.elements.notes.value };
  try {
    const saved = editingCustomer
      ? await api('PUT', `/api/customers/${editingCustomer.id}`, body)
      : await api('POST', '/api/customers', body);
    $('#customer-dialog').close();
    if (!editingCustomer) setView(saved.id, false); // neuen Kunden gleich anzeigen
    await reload();
  } catch (err) {
    showError(form, err);
  }
});

function openCustomerDelete(c) {
  const run = nodes => async () => {
    await api('DELETE', `/api/customers/${c.id}`, nodes ? { nodes } : {});
    if (state.view === String(c.id)) setView('all', false);
    await reload();
  };
  if (c.node_count) {
    openChoice({
      title: 'Kunde löschen',
      text: `„${c.name}“ hat ${c.node_count} ${c.node_count === 1 ? 'Eintrag' : 'Einträge'}. Was soll damit passieren?`,
      choices: [
        { label: 'Nach Allgemein verschieben', run: run('general') },
        { label: 'Mitlöschen', cls: 'danger', run: run('delete') },
      ],
    });
  } else {
    openChoice({ title: 'Kunde löschen', text: `„${c.name}“ löschen?`, choices: [{ label: 'Löschen', cls: 'primary', run: run(null) }] });
  }
}

function fillCustomerSelect(select, selected) {
  select.replaceChildren(
    el('option', { value: '' }, 'Allgemein'),
    ...state.customers.map(c => el('option', { value: String(c.id) }, c.name)),
  );
  select.value = selected == null ? '' : String(selected);
}

// ---------- Import ----------

const importForm = $('#import-form');

function resetImportPreview() {
  $('#import-preview').hidden = true;
  showError(importForm, null);
}

$('#open-import').addEventListener('click', () => {
  importForm.reset();
  fillCustomerSelect(importForm.elements.customer, customerById(state.view) ? state.view : null);
  resetImportPreview();
  $('#import-dialog').showModal();
});

importForm.elements.file.addEventListener('change', resetImportPreview);
importForm.elements.encoding.addEventListener('change', resetImportPreview);
importForm.elements.customer.addEventListener('change', resetImportPreview);

importForm.addEventListener('submit', async e => {
  const action = e.submitter?.value;
  if (action !== 'preview' && action !== 'import') return;
  e.preventDefault();

  const data = new FormData(importForm);
  const select = importForm.elements.customer;
  const scopeName = select.options[select.selectedIndex]?.text ?? 'Allgemein';
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
        ? `– löscht ${p.existing} bestehende Einträge in „${scopeName}“`
        : '';
      $('#import-preview').hidden = false;
    } else {
      const mode = data.get('mode');
      if (mode === 'replace' && !confirm(`Bestand von „${scopeName}“ wirklich ersetzen? Alle bisherigen Einträge dort werden samt Beschreibungen gelöscht.`)) {
        return;
      }
      const s = await upload('/api/import', data);
      $('#import-dialog').close();
      await reload();
      toast(`Importiert nach „${scopeName}“: ${s.folders} Ordner, ${s.transactions} Transaktionen`);
    }
  } catch (err) {
    showError(importForm, err);
  } finally {
    buttonsBusy(false);
  }
});

// ---------- Export für SAP ----------

const sapForm = $('#sap-form');
let sapRequest = 0;

// Werte: "g" = Allgemein, "c:<id>:1" = Kunde + Allgemein, "c:<id>:0" = nur Kunde.
function sapQuery() {
  const value = sapForm.elements.scope.value;
  if (value === 'g') return '';
  const [, id, withGeneral] = value.split(':');
  return queryString({ customer: id, with_general: withGeneral === '1' ? '1' : null });
}

$('#open-sap-export').addEventListener('click', () => {
  const select = sapForm.elements.scope;
  select.replaceChildren(
    el('option', { value: 'g' }, 'Allgemein'),
    ...state.customers.flatMap(c => [
      el('option', { value: `c:${c.id}:1` }, `${c.name} + Allgemein`),
      el('option', { value: `c:${c.id}:0` }, `Nur ${c.name}`),
    ]),
  );
  select.value = customerById(state.view) ? `c:${state.view}:1` : 'g';
  $('#sap-dialog').showModal();
  updateSapPreview();
});

sapForm.elements.scope.addEventListener('change', updateSapPreview);

async function updateSapPreview() {
  const request = ++sapRequest;
  const q = sapQuery();
  $('#sap-download').href = '/api/export/sap' + q;
  showError(sapForm, null);
  $('#sap-summary').textContent = 'Wird geprüft …';
  $('#sap-warnings').hidden = true;
  try {
    const p = await api('GET', '/api/export/sap/preview' + q);
    if (request !== sapRequest) return; // inzwischen anderer Inhalt gewählt
    $('#sap-summary').textContent = `${p.folders} Ordner und ${p.transactions} Transaktionen → ${p.filename}`;
    fillTable($('#sap-warnings'),
      p.warnings.map(w => [[w.tcode || '–'], [w.title], [w.reason]]),
      `${p.warnings.length} ${p.warnings.length === 1 ? 'Eintrag' : 'Einträge'} nicht verlustfrei exportierbar`);
    $('#sap-warnings').open = p.warnings.length > 0;
  } catch (err) {
    if (request !== sapRequest) return;
    $('#sap-summary').textContent = '';
    showError(sapForm, err);
  }
}

// ---------- Kopfzeile, Tastatur ----------

const search = $('#search');

search.addEventListener('input', () => {
  state.query = search.value;
  render();
});

$('#view').addEventListener('change', e => setView(e.target.value));
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
