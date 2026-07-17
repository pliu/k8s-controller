// SPDX-License-Identifier: AGPL-3.0-only
//
// Read-only viewer for ManagedNamespaces and ClusterAccessMappings. Every tab is
// derived from the list endpoints; nothing is mutated. All cluster-supplied
// strings are rendered via textContent to avoid injection.
'use strict';

var namespaces = [];
var cams = [];
function $(id) { return document.getElementById(id); }

function el(tag, text, cls) {
  var e = document.createElement(tag);
  if (text != null) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}
function row(cells, head) {
  var tr = document.createElement('tr');
  cells.forEach(function (c) { tr.appendChild(el(head ? 'th' : 'td', c)); });
  return tr;
}
function table(head, rows) {
  var t = document.createElement('table');
  t.appendChild(row(head, true));
  rows.forEach(function (r) { t.appendChild(row(r)); });
  return t;
}
async function getJSON(url) {
  var r = await fetch(url);
  var t = await r.text();
  if (!r.ok) throw new Error(t || r.status);
  return JSON.parse(t) || [];
}

async function loadData() {
  var both = await Promise.all([
    getJSON('/api/v1/managednamespaces'),
    getJSON('/api/v1/clusteraccessmappings')
  ]);
  namespaces = both[0];
  cams = both[1];
}

function tab(name) {
  ['ns', 'cluster', 'search'].forEach(function (t) {
    $('tab-' + t).classList.toggle('active', name === t);
    $('view-' + t).hidden = name !== t;
  });
}

function nsName(n) { return n.metadata.name; }
function mappings(n) { return (n.spec && n.spec.accessMappings) || []; }

function subjectCells(m) {
  var kind = m.group ? 'Group' : 'User';
  var subject = m.group ? m.group : (m.users || []).join(', ');
  return [subject, kind, (m.clusterRoles || []).join(', ')];
}

function renderList() {
  var list = $('ns-list');
  list.textContent = '';
  if (!namespaces.length) { list.appendChild(el('p', 'No managed namespaces.', 'muted')); return; }
  namespaces.map(nsName).sort().forEach(function (name) {
    var b = el('button', name, 'ns-item');
    b.onclick = function () {
      Array.prototype.forEach.call(document.querySelectorAll('.ns-item'), function (x) { x.classList.remove('sel'); });
      b.classList.add('sel');
      renderDetail(name);
    };
    list.appendChild(b);
  });
}

function renderDetail(name) {
  var n = namespaces.find(function (x) { return nsName(x) === name; });
  var d = $('ns-detail');
  d.textContent = '';
  if (!n) return;
  d.appendChild(el('h3', name));

  d.appendChild(el('h4', 'ResourceQuota'));
  var hard = n.spec && n.spec.resourceQuota && n.spec.resourceQuota.hard;
  if (hard && Object.keys(hard).length) {
    d.appendChild(table(['Resource', 'Hard limit'],
      Object.keys(hard).sort().map(function (k) { return [k, String(hard[k])]; })));
  } else {
    d.appendChild(el('p', 'None', 'muted'));
  }

  d.appendChild(el('h4', 'Permissions'));
  var ms = mappings(n);
  if (!ms.length) { d.appendChild(el('p', 'None', 'muted')); return; }
  d.appendChild(table(['Subject', 'Kind', 'ClusterRoles'], ms.map(subjectCells)));
}

function renderCluster() {
  var out = $('cluster-out');
  out.textContent = '';
  out.appendChild(el('p', 'Cluster-wide grants via ClusterRoleBindings.', 'muted'));
  if (!cams.length) { out.appendChild(el('p', 'No cluster access mappings.', 'muted')); return; }
  var rows = cams.slice().sort(function (a, b) { return a.metadata.name < b.metadata.name ? -1 : 1; })
    .map(function (c) { return [c.metadata.name].concat(subjectCells(c.spec || {})); });
  out.appendChild(table(['Name', 'Subject', 'Kind', 'ClusterRoles'], rows));
}

// match returns the union of ClusterRoles the query is granted by the given
// mappings, plus how it matched (user and/or group).
function match(ms, q) {
  var roles = {}, kinds = {};
  ms.forEach(function (m) {
    var hit = false;
    if (m.group && m.group === q) { hit = true; kinds.group = true; }
    if ((m.users || []).indexOf(q) >= 0) { hit = true; kinds.user = true; }
    if (hit) (m.clusterRoles || []).forEach(function (r) { roles[r] = true; });
  });
  return { roles: Object.keys(roles).sort().join(', '), kinds: Object.keys(kinds).sort().join(', '), any: Object.keys(roles).length > 0 };
}

function search() {
  var q = $('q').value.trim();
  var out = $('search-out');
  out.textContent = '';
  if (!q) return;
  var rows = [];
  namespaces.forEach(function (n) {
    var m = match(mappings(n), q);
    if (m.any) rows.push([nsName(n), m.kinds, m.roles]);
  });
  cams.forEach(function (c) {
    var m = match([c.spec || {}], q);
    if (m.any) rows.push(['cluster-wide', m.kinds, m.roles]);
  });
  if (!rows.length) { out.appendChild(el('p', 'No access found for "' + q + '".', 'muted')); return; }
  rows.sort(function (a, b) { return a[0] < b[0] ? -1 : 1; });
  out.appendChild(table(['Scope', 'Matched as', 'ClusterRoles'], rows));
}

async function refresh() {
  try {
    await loadData();
    renderList();
    renderCluster();
    $('ns-detail').textContent = '';
    $('ns-detail').appendChild(el('p', 'Select a namespace.', 'muted'));
    $('search-out').textContent = '';
  } catch (e) {
    $('ns-list').textContent = '';
    $('ns-list').appendChild(el('p', e.message, 'muted'));
  }
}

refresh();
