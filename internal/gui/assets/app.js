// The window's front end.
//
// It renders and it sends. Every question that has an answer — what she
// remembers, what a tool did, whether a turn is running — is answered on the Go
// side, because a second copy of that in here would be a second answer.
'use strict';

const $ = (id) => document.getElementById(id);
const thread = $('thread');
const input = $('input');
const send = $('send');

let turnEl = null;   // the turn being built
let traceEl = null;  // its foldable "what happened" block
let busy = false;

// ---- rendering ------------------------------------------------------------

function clearEmpty() {
  const e = thread.querySelector('.empty');
  if (e) e.remove();
}

function atBottom() {
  return thread.scrollHeight - thread.scrollTop - thread.clientHeight < 80;
}

function scroll(force) {
  if (force || atBottom()) thread.scrollTop = thread.scrollHeight;
}

function userTurn(text) {
  clearEmpty();
  const el = document.createElement('div');
  el.className = 'turn user';
  const b = document.createElement('div');
  b.className = 'bubble';
  b.textContent = text;            // textContent, never innerHTML: this is input
  el.appendChild(b);
  thread.appendChild(el);
  scroll(true);
}

function beginFreyaTurn() {
  clearEmpty();
  turnEl = document.createElement('div');
  turnEl.className = 'turn freya';

  const who = document.createElement('div');
  who.className = 'who';
  who.textContent = 'F';

  const body = document.createElement('div');
  body.className = 'body';

  traceEl = document.createElement('details');
  traceEl.className = 'trace';
  const sum = document.createElement('summary');
  sum.textContent = 'Working…';
  const inner = document.createElement('div');
  inner.className = 'inner';
  traceEl.append(sum, inner);

  const working = document.createElement('div');
  working.className = 'working';
  working.innerHTML = '<span class="pulse"></span>';
  working.append(document.createTextNode('thinking'));

  body.append(traceEl, working);
  turnEl.append(who, body);
  thread.appendChild(turnEl);
  scroll(true);
}

function trace(node) {
  if (!traceEl) return;
  traceEl.querySelector('.inner').appendChild(node);
  scroll(false);
}

function addThought(text) {
  const p = document.createElement('p');
  p.className = 'thought';
  p.textContent = text;
  trace(p);
}

function addStep(name, ok) {
  if (!traceEl) return;
  const inner = traceEl.querySelector('.inner');
  // An existing pending row for this tool is the one being resolved.
  let row = ok === undefined ? null
    : [...inner.querySelectorAll('.step')].reverse()
        .find((r) => r.dataset.name === name && !r.classList.contains('ok') && !r.classList.contains('bad'));
  if (!row) {
    row = document.createElement('div');
    row.className = 'step';
    row.dataset.name = name;
    row.innerHTML = '<span class="dot"></span>';
    const n = document.createElement('span');
    n.className = 'name';
    n.textContent = name;
    row.appendChild(n);
    inner.appendChild(row);
  }
  if (ok === true) row.classList.add('ok');
  if (ok === false) row.classList.add('bad');
  scroll(false);
}

// Markdown, the part of it she actually writes.
//
// Fences, inline code, bold, italic, headings and both kinds of list. Not a
// parser — a parser in here is a dependency by another name — but her replies
// are full of lists and bold labels, and showing those raw turns an answer into
// a dump.
//
// Every leaf is inserted as text. Nothing a model or an archive produces reaches
// innerHTML, so a reply containing markup is a reply containing markup.

// inline fills an element with text, code, bold and italic.
function inline(parent, text) {
  // Split on the three inline forms at once, keeping the delimiters' contents.
  const re = /(`[^`]+`|\*\*[^*]+\*\*|\*[^*\n]+\*)/g;
  let last = 0;
  for (const m of text.matchAll(re)) {
    if (m.index > last) parent.appendChild(document.createTextNode(text.slice(last, m.index)));
    const tok = m[0];
    let el;
    if (tok.startsWith('`')) { el = document.createElement('code'); el.textContent = tok.slice(1, -1); }
    else if (tok.startsWith('**')) { el = document.createElement('strong'); el.textContent = tok.slice(2, -2); }
    else { el = document.createElement('em'); el.textContent = tok.slice(1, -1); }
    parent.appendChild(el);
    last = m.index + tok.length;
  }
  if (last < text.length) parent.appendChild(document.createTextNode(text.slice(last)));
}

// blocks turns one prose chunk into paragraphs, headings and lists.
function blocks(body, chunk) {
  const lines = chunk.split('\n');
  let list = null;      // the <ul>/<ol> being filled
  let para = null;      // the <p> being filled

  const endPara = () => { para = null; };
  const endList = () => { list = null; };

  for (const raw of lines) {
    const line = raw.trimEnd();
    if (!line.trim()) { endPara(); endList(); continue; }

    const heading = line.match(/^(#{1,4})\s+(.*)$/);
    if (heading) {
      endPara(); endList();
      const h = document.createElement('h3');
      inline(h, heading[2]);
      body.appendChild(h);
      continue;
    }

    const bullet = line.match(/^\s*[-*+]\s+(.*)$/);
    const number = line.match(/^\s*\d+[.)]\s+(.*)$/);
    if (bullet || number) {
      endPara();
      const want = bullet ? 'UL' : 'OL';
      if (!list || list.tagName !== want) {
        list = document.createElement(bullet ? 'ul' : 'ol');
        body.appendChild(list);
      }
      const li = document.createElement('li');
      inline(li, (bullet || number)[1]);
      list.appendChild(li);
      continue;
    }

    endList();
    if (!para) { para = document.createElement('p'); body.appendChild(para); }
    else para.appendChild(document.createTextNode(' '));
    inline(para, line);
  }
}

function renderReply(text) {
  if (!turnEl) beginFreyaTurn();
  const body = turnEl.querySelector('.body');
  body.querySelector('.working')?.remove();
  if (traceEl) {
    const steps = traceEl.querySelectorAll('.step').length;
    traceEl.querySelector('summary').textContent =
      steps ? `${steps} step${steps === 1 ? '' : 's'}` : 'What she was thinking';
    if (!traceEl.querySelector('.inner').children.length) traceEl.remove();
  }

  text.split(/```/).forEach((part, i) => {
    if (i % 2 === 1) {
      const pre = document.createElement('pre');
      const code = document.createElement('code');
      code.textContent = part.replace(/^[a-zA-Z0-9_+-]*\n/, '');
      pre.appendChild(code);
      body.appendChild(pre);
      return;
    }
    blocks(body, part);
  });
  scroll(true);
}

function renderError(text) {
  if (!turnEl) beginFreyaTurn();
  const body = turnEl.querySelector('.body');
  body.querySelector('.working')?.remove();
  const d = document.createElement('div');
  d.className = 'failed';
  d.textContent = text;
  body.appendChild(d);
  scroll(true);
}

// ---- the stream -----------------------------------------------------------

function connect() {
  const conn = $('conn');
  const src = new EventSource('/events');

  src.onopen = () => { conn.textContent = 'connected'; conn.className = 'conn live'; };
  src.onerror = () => {
    conn.textContent = 'reconnecting'; conn.className = 'conn gone';
    // EventSource retries on its own; saying so is the whole job here.
  };
  src.onmessage = (m) => {
    let e;
    try { e = JSON.parse(m.data); } catch { return; }
    switch (e.kind) {
      case 'thought':  addThought(e.text); break;
      case 'interim':  addThought(e.text); break;
      case 'tool':     addStep(e.name, e.ok); break;
      case 'reply':    renderReply(e.text); break;
      case 'error':    renderError(e.text); break;
      case 'done':     finish(); break;
    }
  };
}

function finish() {
  busy = false;
  send.disabled = false;
  $('status').textContent = '';
  turnEl?.querySelector('.working')?.remove();
  turnEl = null;
  traceEl = null;
  input.focus();
  loadHistory();
}

// ---- sending --------------------------------------------------------------

async function ask(text) {
  busy = true;
  send.disabled = true;
  $('status').textContent = 'working';
  userTurn(text);
  beginFreyaTurn();
  try {
    const r = await fetch('/ask', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text }),
    });
    if (!r.ok) {
      renderError(await r.text() || `refused (${r.status})`);
      finish();
    }
  } catch (err) {
    renderError(String(err));
    finish();
  }
}

function grow() {
  input.style.height = 'auto';
  input.style.height = Math.min(input.scrollHeight, 200) + 'px';
}

$('composer').addEventListener('submit', (ev) => {
  ev.preventDefault();
  const text = input.value.trim();
  if (!text || busy) return;
  input.value = '';
  grow();
  ask(text);
});

input.addEventListener('input', grow);
input.addEventListener('keydown', (ev) => {
  if (ev.key === 'Enter' && !ev.shiftKey) {
    ev.preventDefault();
    $('composer').requestSubmit();
  }
});

$('theme').addEventListener('click', () => {
  const root = document.documentElement;
  const next = root.dataset.theme === 'dark' ? 'light' : 'dark';
  root.dataset.theme = next;
  try { localStorage.setItem('freya-theme', next); } catch {}
});

// ---- history --------------------------------------------------------------

let current = null;

async function loadHistory() {
  const rail = $('history');
  try {
    const r = await fetch('/history');
    const list = await r.json();
    rail.textContent = '';
    if (!list.length) {
      const none = document.createElement('div');
      none.className = 'none';
      none.textContent = 'Nothing yet.';
      rail.appendChild(none);
      return;
    }
    for (const c of list) {
      const item = document.createElement('div');
      item.className = 'item' + (c.id === current ? ' on' : '');
      item.textContent = c.title;
      item.title = `${c.at} · ${c.turns} turn${c.turns === 1 ? '' : 's'}`;
      item.addEventListener('click', () => openConversation(c.id, c.title));
      rail.appendChild(item);
    }
  } catch {
    // The rail is a convenience. Failing to draw it must not take the window
    // down, and the conversation still works without it.
  }
}

async function openConversation(id, title) {
  if (busy) return;
  current = id;
  $('title').textContent = title;
  thread.textContent = '';
  try {
    const r = await fetch('/conversation?id=' + encodeURIComponent(id));
    const turns = await r.json();
    for (const t of turns) {
      if (t.role === 'user') userTurn(t.text);
      else if (t.role === 'assistant') { beginFreyaTurn(); renderReply(t.text); turnEl = null; traceEl = null; }
    }
    scroll(true);
  } catch {
    renderError('That conversation could not be read.');
  }
  loadHistory();
}

$('new-chat').addEventListener('click', () => location.reload());

try {
  const saved = localStorage.getItem('freya-theme');
  if (saved) document.documentElement.dataset.theme = saved;
} catch {}

connect();
loadHistory();
input.focus();
