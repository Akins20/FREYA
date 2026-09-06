// The window's front end.
//
// It renders and it sends. Every question that has an answer — what she
// remembers, what a tool did, whether a turn is running — is answered on the Go
// side, because a second copy of that in here would be a second answer.
'use strict';

// A missing element is a null dereference, and inside an async handler the
// rejection goes nowhere — which is how the whole activity panel once shipped
// absent with no error anywhere. Say so instead.
const $ = (id) => {
  const el = document.getElementById(id);
  if (!el) console.error('freya: the page has no #' + id);
  return el;
};
window.addEventListener('unhandledrejection', (e) => {
  console.error('freya: unhandled', e.reason);
});
const thread = $('thread');
const input = $('input');
const send = $('send');

let turnEl = null;   // the turn being built
let busy = false;

// The parts of the turn her working is drawn into. One turn at a time, so these
// are module state rather than threaded through every function.
let workEl = null, flowEl = null, barEl = null, sayEl = null, mindEl = null;
let openGrp = null;             // the group currently accepting calls
const openCalls = new Map();    // tool name -> [{el, t0}], resolved FIFO
let mindCount = 0, turnT0 = 0, tick = 0, span = 0;

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

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;   // never innerHTML
  return n;
}

function userTurn(text) {
  clearEmpty();
  const t = el('div', 'turn user');
  t.appendChild(el('div', 'bubble', text));
  thread.appendChild(t);
  scroll(true);
}

// ---- a turn has three states ----------------------------------------------
//
// One attribute, data-phase, on one element. The same subtree is rendered three
// ways by CSS and nothing is created, destroyed or re-parented between them.
//
//   live     she is working and there is no reply yet — the trace IS the content
//   fresh    the reply has landed and this is still the last turn — one click back
//   settled  a newer turn exists below — a column of hairline ticks in the gutter
//
// The demotion fires when the NEXT turn begins, so the thread quiets behind you
// as you go. That single mechanic is most of the answer to "cluttered": ten
// turns of scrollback were carrying ten bordered boxes summarising work nobody
// is looking at any more.

function settle(w) {
  if (!w || w.dataset.phase === 'settled') return;
  w.dataset.phase = 'settled';
  w.closest('.turn')?.classList.add('settled');
  w.tabIndex = 0;
  w.setAttribute('role', 'button');
}

// A settled turn comes back on a click or a key, in place. One way in, one class
// swap — rather than a pile of :focus-within overrides that fight each other.
function unsettle(w) {
  if (!w || w.dataset.phase !== 'settled') return;
  w.dataset.phase = 'fresh';
  w.closest('.turn')?.classList.remove('settled');
  w.removeAttribute('tabindex');
  w.removeAttribute('role');
}

function beginFreyaTurn() {
  clearEmpty();
  // The whole fresh -> settled mechanic, in one line.
  thread.querySelectorAll('.work[data-phase="fresh"]').forEach(settle);

  turnEl = el('div', 'turn freya');

  workEl = el('aside', 'work');
  workEl.dataset.phase = 'live';

  barEl = el('button', 'work-bar');
  barEl.type = 'button';
  barEl.setAttribute('aria-expanded', 'true');
  const dot = el('span', 'work-dot');
  sayEl = el('span', 'work-say');
  sayEl.dataset.kind = 'thought';
  const time = el('span', 'work-time');
  barEl.append(dot, sayEl, time);

  flowEl = el('ol', 'flow');

  // Her thinking, kept but not accumulated on screen. A thought is the most
  // disposable thing in this window — interesting for the four seconds it is
  // true — and the old view did the opposite: every one of them piled up inside
  // a fold that was both closed and enormous.
  mindEl = el('details', 'mind');
  mindEl.append(el('summary', '', ''), el('div', 'mind-log'));

  const body = el('div', 'body');
  workEl.append(barEl, flowEl, mindEl);
  turnEl.append(workEl, body);
  thread.appendChild(turnEl);

  openGrp = null;
  openCalls.clear();
  mindCount = 0;
  turnT0 = now();
  span = 0;
  clearInterval(tick);
  tick = setInterval(tickClock, 250);

  scroll(true);
  return turnEl;
}

// now is monotonic and never crosses the wire: every --s and --e is unitless
// milliseconds since this turn began.
function now() { return performance.now(); }

// ---- thinking, which never touches the waterfall ---------------------------
//
// say() writes the one-line slot and the log. addCall() writes the flow. There
// is no longer a function that both call, which is what let thoughts and tool
// calls end up in the same box in the first place.

// Her reasoning arrives as markdown — "**My Approach to the Task**" — and this
// slot is one line of plain text, so the markers have to come off rather than be
// shown raw. Not a parser: the emphasis carries nothing here, so it is removed
// rather than rendered.
function plain(text) {
  return String(text || '')
    .replace(/`{1,3}/g, '')
    .replace(/\*{1,3}/g, '')
    .replace(/^#{1,6}\s*/gm, '')
    .replace(/^[-*]\s+/gm, '')
    .trim();
}

function say(kind, text) {
  if (!workEl) beginFreyaTurn();
  const line = plain(text);
  if (!line) return;
  sayEl.textContent = line.split('\n').map((l) => l.trim()).find(Boolean) || line;
  sayEl.dataset.kind = kind;

  const p = el('p', 'mote', line);
  p.dataset.kind = kind;
  mindEl.querySelector('.mind-log').appendChild(p);
  mindCount += 1;
  mindEl.querySelector('summary').textContent =
    mindCount + (mindCount === 1 ? ' thought' : ' thoughts');
  scroll(false);
}

// ---- the waterfall ---------------------------------------------------------

// family is the tool's subsystem: the name up to the first underscore. It is the
// registry's own convention — browser_*, file_*, desktop_* — so a group header
// makes a true statement about a subsystem rather than a vague "6 things". A
// name with no underscore is its own family and only groups with an exact repeat.
function family(name) {
  const i = name.indexOf('_');
  return i > 0 ? name.slice(0, i) : name;
}

const FAMILY_LABEL = {
  browser: 'Browser', file: 'Files', folder: 'Files', run: 'Shell',
  terminal: 'Terminal', desktop: 'Desktop', web: 'Web', memory: 'Memory',
  recall: 'Memory', plan: 'Plan', service: 'Services', usage: 'Usage',
  claude: 'Claude', work: 'Jobs', dev: 'Code', system: 'System', note: 'Notes',
  screen: 'Screen', image: 'Images', doc: 'Documents', site: 'Site',
};

// An unknown prefix prints itself rather than nothing, so a tool added tomorrow
// degrades to a plain label instead of an empty header.
function famLabel(fam) { return FAMILY_LABEL[fam] || fam; }

const GROUP_CAP = 24;   // beyond this a sibling group of the same family opens

function closeGroup() {
  if (!openGrp) return;
  const g = openGrp;
  openGrp = null;
  const state = groupState(g.li);
  // A group that ran clean folds itself away the moment she moves on. A group
  // with a failure in it never folds, in either direction — a failure you have
  // to click to find is a failure you do not see.
  if (state === 'ok' && g.n > 1) g.details.open = false;
}

function groupState(li) {
  const kids = [...li.querySelectorAll('.entry.call')];
  const state = kids.some((k) => k.dataset.state === 'run') ? 'run'
    : kids.some((k) => k.dataset.state === 'bad') ? 'bad'
    : kids.some((k) => k.dataset.state === 'stale') ? 'stale' : 'ok';
  li.dataset.state = state;
  return state;
}

function callRow(name, arg) {
  const li = el('li', 'entry call');
  li.dataset.name = name;
  li.dataset.state = 'run';
  li.append(el('span', 'node'), el('span', 'call-name', name));
  const a = el('span', 'call-arg', arg || '');
  a.title = arg || '';
  const bar = el('span', 'bar');
  bar.appendChild(el('i', 'bar-fill'));
  li.append(a, bar, el('span', 'call-ms'));
  li.title = arg ? name + '  ' + arg : name;
  return li;
}

// The second consecutive call of a family promotes the lone row that is already
// there into a group: one node move, at the bottom of the list, before anything
// below it exists. openCalls holds element references, so a finish still in
// flight resolves correctly across the move.
function promoteToGroup(first, fam) {
  const li = el('li', 'entry grp');
  li.dataset.fam = fam;
  li.dataset.state = 'run';

  const details = el('details', '');
  details.open = true;
  const head = el('summary', 'grp-head');
  head.append(el('span', 'node'), el('span', 'grp-name', famLabel(fam)),
    el('span', 'grp-n', '×2'));
  const bar = el('span', 'bar rollup');
  bar.appendChild(el('i', 'bar-fill'));
  head.append(bar, el('span', 'grp-ms'));

  const body = el('ol', 'grp-body');
  first.replaceWith(li);
  body.appendChild(first);
  details.append(head, body);
  li.appendChild(details);

  return { li, details, body, head, fam, n: 1 };
}

function addCall(name, ok, text, call) {
  // Today's addStep returned early with no turn and the event was simply lost.
  if (!workEl) beginFreyaTurn();
  const key = call || name;

  if (ok === undefined) {
    const fam = family(name);
    const row = callRow(name, text);
    row.style.setProperty('--s', Math.round(now() - turnT0));
    row.style.setProperty('--e', Math.round(now() - turnT0));

    const last = flowEl.lastElementChild;
    if (openGrp && openGrp.fam === fam && openGrp.li === last && openGrp.n < GROUP_CAP) {
      openGrp.body.appendChild(row);
      openGrp.n += 1;
      openGrp.head.querySelector('.grp-n').textContent = '×' + openGrp.n;
      nameGroup(openGrp);
    } else if (!openGrp && last && last.classList.contains('call') &&
               family(last.dataset.name) === fam) {
      // Two in a row of one family: promote, then add.
      openGrp = promoteToGroup(last, fam);
      openGrp.body.appendChild(row);
      openGrp.n = 2;
      nameGroup(openGrp);
    } else {
      closeGroup();
      flowEl.appendChild(row);
    }

    if (!openCalls.has(key)) openCalls.set(key, []);
    openCalls.get(key).push(row);
    rescale();
    scroll(false);
    return;
  }

  // A finish. FIFO among unfinished rows of this name — the old code took the
  // LAST one, which resolves concurrent calls to one tool by luck.
  const queue = openCalls.get(key) || [];
  const row = queue.shift() || (() => {
    // A finish with no start: Emit dropped one. Draw it resolved, with no bar
    // and no duration, rather than losing it.
    const r = callRow(name, '');
    r.dataset.orphan = '';
    flowEl.appendChild(r);
    return r;
  })();
  if (!queue.length) openCalls.delete(key);

  row.dataset.state = ok ? 'ok' : 'bad';
  const ms = Math.round(now() - turnT0);
  row.style.setProperty('--e', ms);
  const started = Number(row.style.getPropertyValue('--s') || 0);
  // Every row gets its duration now: e.call identifies the invocation, so a
  // finish is paired with its own start even when six of one tool are in flight
  // at once. Without it the pairing was a guess by name, and the window drew six
  // overlapping bars it could not attribute.
  if (row.dataset.orphan === undefined) {
    row.querySelector('.call-ms').textContent = fmtMs(ms - started);
  }
  if (!ok && text) {
    const why = el('div', 'call-why', text);
    row.appendChild(why);
    workEl.dataset.fault = '';
    row.closest('details')?.setAttribute('open', '');
  }
  const grp = row.closest('.entry.grp');
  if (grp) {
    groupState(grp);
    rollup(grp);
  }
  rescale();
  scroll(false);
}

// A retry is not a tool and never gets a tick. It is the seam where she looked
// at what she had and decided to go round again — which is exactly the moment a
// group must not fold across, or the fold hides the thing worth seeing.
function addMark(why, detail) {
  if (!workEl) beginFreyaTurn();
  closeGroup();
  const li = el('li', 'entry mark');
  li.append(el('span', 'node node-mark'),
    el('span', 'mark-why', 'going round again — ' + (why || 'unfinished')));
  if (detail) li.title = detail;
  flowEl.appendChild(li);
  scroll(false);
}

// When every member shares one name the header says the tool; mixed families say
// the subsystem. "browser_scroll ×12" is more useful than "Browser ×12".
function nameGroup(g) {
  const names = new Set([...g.body.querySelectorAll('.entry.call')].map((c) => c.dataset.name));
  g.head.querySelector('.grp-name').textContent =
    names.size === 1 ? [...names][0] : famLabel(g.fam);
}

// The group's envelope: from the first member's start to the last one's end, on
// the same ruler as its members, so collapsing is a zoom-out rather than a
// substitution.
function rollup(grp) {
  const kids = [...grp.querySelectorAll('.entry.call')];
  if (!kids.length) return;
  let lo = Infinity, hi = 0;
  for (const k of kids) {
    lo = Math.min(lo, Number(k.style.getPropertyValue('--s') || 0));
    hi = Math.max(hi, Number(k.style.getPropertyValue('--e') || 0));
  }
  grp.style.setProperty('--s', lo);
  grp.style.setProperty('--e', hi);
  const ms = grp.querySelector('.grp-ms');
  if (ms && grp.dataset.state !== 'run') ms.textContent = fmtMs(hi - lo);
}

// ---- the axis --------------------------------------------------------------
//
// One axis per turn, not per group: normalising each group to full width would
// make a 40ms group look identical to a 40-second one, which is a lie in exactly
// the place a waterfall exists to tell the truth.
//
// The span grows in 1.5x steps and never shrinks within a turn, so bars settle
// in occasional jumps instead of creeping under the cursor every quarter second.
function spanFor(ms) {
  let s = 1000;
  while (s < ms) s = Math.ceil(s * 1.5);
  return s;
}

function rescale() {
  if (!flowEl) return;
  let hi = 0;
  for (const e of flowEl.querySelectorAll('.entry')) {
    hi = Math.max(hi, Number(e.style.getPropertyValue('--e') || 0));
  }
  const want = spanFor(hi);
  if (want > span) {
    span = want;
    flowEl.style.setProperty('--scale', 100 / span);
  }
  // A single timed entry has nothing to compare against, so a bar spanning the
  // whole track would say nothing. The column collapses and the duration stands
  // on its own.
  const timed = flowEl.querySelectorAll('.entry.call, .entry.grp').length;
  flowEl.dataset.bars = timed > 1 ? 'on' : 'off';
}

function fmtMs(ms) {
  if (ms < 0) return '';
  if (ms < 1000) return Math.round(ms) + 'ms';
  return (ms / 1000).toFixed(ms < 10000 ? 1 : 0) + 's';
}

function fmtClock(ms) {
  const t = Math.floor(ms / 1000);
  return Math.floor(t / 60) + ':' + String(t % 60).padStart(2, '0');
}

// One ticker for the window: it grows the running bars, steps the axis, and
// writes the clock. Started with the turn and cleared by sealWork, which is the
// single place a turn stops running — so it cannot be left writing into a
// detached element.
function tickClock() {
  if (!workEl || workEl.dataset.phase !== 'live') return;
  const t = Math.round(now() - turnT0);
  for (const e of flowEl.querySelectorAll('.entry[data-state="run"]')) {
    e.style.setProperty('--e', t);
  }
  for (const g of flowEl.querySelectorAll('.entry.grp[data-state="run"]')) rollup(g);
  rescale();
  barEl.querySelector('.work-time').textContent = fmtClock(t);
}

// ---- she is not working any more -------------------------------------------

function summarise() {
  const calls = flowEl.querySelectorAll('.entry.call').length;
  // Deduplicated on the LABEL, not the family: file_* and folder_* are different
  // prefixes and the same subsystem, so a set of families produced
  // "system, files, files".
  const seen = new Set();
  for (const c of flowEl.querySelectorAll('.entry.call')) seen.add(famLabel(family(c.dataset.name)));
  if (!calls) return mindCount ? 'thought it through' : '';
  const what = [...seen].join(', ').toLowerCase();
  return calls + (calls === 1 ? ' step' : ' steps') + (what ? ' · ' + what : '');
}

// The one place a running turn stops running. Every path that ends a turn calls
// it, so they cannot drift apart.
function sealWork() {
  clearInterval(tick);
  tick = 0;
  if (!workEl || workEl.dataset.phase !== 'live') return;
  closeGroup();
  // A call still open when the turn ended is not "not started" and is not
  // "finished": it was cut off. It says so, with a hollow node and a dashed bar.
  for (const e of flowEl.querySelectorAll('.entry[data-state="run"]')) e.dataset.state = 'stale';
  for (const g of flowEl.querySelectorAll('.entry.grp')) { groupState(g); rollup(g); }
  rescale();

  const label = summarise();
  sayEl.textContent = label;
  sayEl.removeAttribute('data-kind');
  barEl.setAttribute('aria-label', label || 'what she did');
  barEl.querySelector('.work-time').textContent = fmtClock(now() - turnT0);
  workEl.title = label;
  workEl.dataset.phase = 'fresh';
  workEl.classList.remove('open');
  barEl.setAttribute('aria-expanded', 'false');

  // A replayed archive turn has no trace at all; it should render as prose, not
  // as an empty box with a caret.
  if (!flowEl.children.length && !mindCount) workEl.remove();
}

function endTurn() {
  turnEl = null;
  workEl = null;
  flowEl = null;
  barEl = null;
  sayEl = null;
  mindEl = null;
  openGrp = null;
  openCalls.clear();
}

// Opening a finished turn's working, and promoting a settled one back.
thread.addEventListener('click', (ev) => {
  const w = ev.target.closest('.work');
  if (!w) return;
  if (w.dataset.phase === 'settled') { unsettle(w); return; }
  if (ev.target.closest('.work-bar')) {
    const open = w.classList.toggle('open');
    w.querySelector('.work-bar').setAttribute('aria-expanded', String(open));
  }
});
thread.addEventListener('keydown', (ev) => {
  if (ev.key !== 'Enter' && ev.key !== ' ') return;
  const w = ev.target.closest?.('.work[data-phase="settled"]');
  if (!w) return;
  ev.preventDefault();
  unsettle(w);
});

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
function blocks(body, chunk, depth) {
  depth = depth || 0;
  const lines = chunk.split('\n');
  let list = null;      // the <ul>/<ol> being filled
  let para = null;      // the <p> being filled
  let quote = null;     // the lines of the > block being collected

  const endPara = () => { para = null; };
  const endList = () => { list = null; };

  // A quote is collected whole and then parsed as its own document, so a list or
  // a second paragraph inside one comes out as a list or a second paragraph.
  const endQuote = () => {
    if (!quote) return;
    const q = document.createElement('blockquote');
    body.appendChild(q);
    // Bounded. She writes quotes; she does not write quotes of quotes of quotes,
    // and a reply that did would otherwise recurse as deep as it liked.
    if (depth < 3) blocks(q, quote.join('\n'), depth + 1);
    else q.appendChild(document.createTextNode(quote.join(' ')));
    quote = null;
  };

  for (const raw of lines) {
    const line = raw.trimEnd();
    if (!line.trim()) { endPara(); endList(); endQuote(); continue; }

    // Blockquotes. Without this branch a quoted line fell through to the
    // paragraph case and consecutive ones were joined with spaces, so a page she
    // had quoted came out as one run-on line with the > markers still in it.
    const quoted = line.match(/^\s*>\s?(.*)$/);
    if (quoted) {
      endPara(); endList();
      if (!quote) quote = [];
      quote.push(quoted[1]);
      continue;
    }
    endQuote();

    // A rule. Three or more of - * _ on their own line, which is not a bullet
    // because a bullet needs a space after it — so this used to print as "---".
    if (/^\s*([-*_])\s*(\1\s*){2,}$/.test(line)) {
      endPara(); endList();
      body.appendChild(document.createElement('hr'));
      continue;
    }

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
  endQuote();
}

// The body of a turn, with her working sealed behind it.
function turnBody() {
  if (!turnEl) beginFreyaTurn();
  const body = turnEl.querySelector('.body');
  sealWork();
  return body;
}

function renderReply(text) {
  const body = turnBody();
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

// Superseded is not failed. Something else took the turn — a spoken request, the
// stop word — and a red failure box would be wrong about what happened.
function renderStopped(text) {
  turnBody().appendChild(el('div', 'note', text || 'Stopped.'));
  scroll(true);
}

function renderError(text) {
  turnBody().appendChild(el('div', 'failed', text));
  scroll(true);
}

// A note in the thread rather than a line in a status bar that vanishes. Used
// for the things the window itself has to say — a control that would not work,
// an answer that arrived too late.
function renderNote(text) {
  clearEmpty();
  const t = el('div', 'turn aside-note');
  t.appendChild(el('div', 'note', text));
  thread.appendChild(t);
  scroll(true);
}

// ---- the stream -----------------------------------------------------------

function connect() {
  const conn = $('conn');
  const src = new EventSource('/events');

  src.onopen = () => {
    conn.textContent = 'connected';
    conn.className = 'conn live';
    document.body.classList.remove('offline');
    pollState();
  };
  src.onerror = () => {
    conn.textContent = 'reconnecting';
    conn.className = 'conn gone';
    // EventSource retries on its own; saying so is the whole job here.
    //
    // The panels are marked stale as well. They keep showing whatever they last
    // read, which is right — throwing the numbers away would be worse — but a
    // status display that goes on looking live while its source is gone is the
    // exact failure it exists to prevent.
    document.body.classList.add('offline');
  };
  src.onmessage = (m) => {
    let e;
    try { e = JSON.parse(m.data); } catch { return; }
    switch (e.kind) {
      case 'thought':  say('thought', e.text); break;
      case 'interim':  say('interim', e.text); break;
      case 'tool':     addCall(e.name, e.ok, e.text, e.call); break;
      // Her deciding to go round again. It has been on the wire and dropped on
      // the floor here, with no case at all.
      case 'retry':    addMark(e.name, e.text); break;
      case 'reply':    renderReply(e.text); break;
      case 'error':    renderError(e.text); break;
      case 'stopped':  renderStopped(e.text); break;
      case 'done':     finish(); break;
      case 'confirm':         askPermission(e.text); break;
      case 'confirm-timeout': settlePerm(e.text, 'expired'); break;
      case 'listening':       micState('listening'); break;
      case 'heard':           spokenTurn(e.text); break;
      case 'speaking':        micState(e.text ? 'speaking' : ''); break;
    }
  };
}




// ---- talking to her -------------------------------------------------------
//
// The window has no microphone of its own. It presses a button and the process
// that owns the archive does the recording, the transcription, the speaker
// verification and the speaking — the same pipeline the Ctrl+Space hotkey uses.
// A microphone opened in here would be a second recorder fighting the first for
// one device, and audio arriving from a web page skips the voiceprint that
// decides whose instructions she takes.
//
// It is tap-to-talk, not hold-to-talk: the recorder stops when you stop, so the
// button is pressed once and then spoken at. The label says so.

let voiceOn = false;
let listening = false;   // the recorder is open, whatever else is happening

// The microphone button IS the indicator. There used to be a second one in the
// topbar saying the same word, and a third in the inspector's voice pill.
function micState(state) {
  listening = state === 'listening';
  const mic = $('mic');
  mic.classList.toggle('listening', state === 'listening');
  mic.classList.toggle('speaking', state === 'speaking');
}

// What she heard, arriving as the user's turn — because that is what it is. It
// also puts a mis-transcription in front of the person who can see it is wrong,
// beside the answer it produced.
function spokenTurn(text) {
  if (!text) return;
  // A spoken request supersedes whatever was running — beginTurn cancels it on
  // her side. The bubble it was writing into has to be closed here, or the
  // reply to THIS request lands in the previous turn's body and then finish()
  // tears down the wrong element.
  if (busy && turnEl) {
    // Seal before endTurn: this is the path that clears the ticker, and without
    // it a detached interval goes on writing into an orphaned element forever.
    sealWork();
    endTurn();
  }
  listening = false;
  micState('');
  busy = true;
  send.disabled = true;
  $('stop').hidden = false;
  userTurn(text);
  beginFreyaTurn();
  pollState();
}

async function control(path) {
  try {
    const r = await fetch('/voice/' + path, { method: 'POST' });
    if (!r.ok) {
      // Into the thread, not a status line that clears itself after four
      // seconds. "The microphone is in use" is worth still being there when you
      // look back at why the button did nothing.
      renderNote((await r.text()).trim() || 'That did not work.');
      return null;
    }
    return await r.json();
  } catch (err) {
    renderError(String(err));
    return null;
  }
}

$('mic').addEventListener('click', async () => {
  micState('listening');
  const r = await control('talk');
  if (!r) micState('');
});

$('voice').addEventListener('click', async () => {
  const r = await control(voiceOn ? 'off' : 'on');
  if (r) setVoiceButton(!!r.voice);
});

function setVoiceButton(on) {
  voiceOn = on;
  $('voice').setAttribute('aria-pressed', String(on));
  $('voice').classList.toggle('on', on);
}

$('stop').addEventListener('click', async () => {
  const r = await control('stop');
  if (r && r.stopped) renderError(r.stopped);
  finish();
});

// ---- what she has on ------------------------------------------------------
//
// One poll, one struct, one render. Six endpoints drawing six panels is six
// chances for them to disagree about what she is doing, and this is a status
// display — the whole value of it is that it is consistent with itself.
//
// Polled rather than streamed on purpose. The event stream carries what happens
// during a turn; this is what is *true* between them, and a panel that only
// updated when a turn happened to emit something would go stale exactly when
// nothing is going on, which is when someone looks at it.

const NOTICED_SHOWN = 5;   // beyond this the panel is noise, not news
const INSPECT_IDLE = 6000;  // nothing running: slow enough to be free
const INSPECT_BUSY = 1500;  // mid-turn: fast enough that the plan ticks over

let inspectTimer = 0;
let inspectOn = true;
try { inspectOn = localStorage.getItem('freya.inspect') !== 'off'; } catch {}

function panel(title, count) {
  const sec = document.createElement('section');
  sec.className = 'panel';
  const h = document.createElement('h4');
  h.textContent = title;
  if (count) {
    const n = document.createElement('span');
    n.className = 'count';
    n.textContent = count;
    h.appendChild(n);
  }
  sec.appendChild(h);
  const body = document.createElement('div');
  body.className = 'panel-body';
  sec.appendChild(body);
  return { sec, body };
}

function row(body, text, sub, cls) {
  const d = document.createElement('div');
  d.className = 'row' + (cls ? ' ' + cls : '');
  const t = document.createElement('div');
  t.className = 'row-main';
  t.textContent = text;
  d.appendChild(t);
  if (sub) {
    const s = document.createElement('div');
    s.className = 'row-sub';
    s.textContent = sub;
    d.appendChild(s);
  }
  body.appendChild(d);
  return d;
}

function renderState(st) {
  // The parts that must run whatever the panel is doing: a question she is
  // waiting on, and the voice toggle following her rather than its own clicks.
  for (const q of st.asking || []) askPermission(JSON.stringify({ ...q, replayed: true }));
  reconcileAsks(st.asking);
  setVoiceButton(st.voice && st.voice !== 'off');
  if (!inspectOn || !inspectorFits()) return;

  $('ins-voice').textContent = st.voice || 'off';
  $('ins-voice').className = 'pill voice-' + (st.voice || 'off');
  $('ins-cost').textContent = st.costToday
    ? '$' + st.costToday.toFixed(2) + ' · ' + (st.callsToday || 0) + ' calls'
    : 'nothing spent yet';
  // The provider already names the model it is configured with, so printing both
  // reads "gemini/gemini-3.5-flash-lite · gemini-3.5-flash-lite".
  const provider = st.provider || '';
  $('ins-model').textContent = st.model && !provider.includes(st.model)
    ? [provider, st.model].filter(Boolean).join(' · ')
    : provider || st.model || '';

  // The event stream can drop a confirm — a reconnect, a full channel — and a
  // dropped one reads to her as a refusal five minutes later. The poll carries
  // the outstanding question too, so a window that missed it still gets asked.
  const panels = $('panels');
  panels.textContent = '';

  // The plan first and always, when there is one. It is the answer to "what is
  // she doing", and everything below it is the answer to "what else is there".
  if (st.plan && st.plan.length) {
    const done = st.plan.filter((s) => s.state === 'done').length;
    const p = panel('Plan', done + '/' + st.plan.length);
    st.plan.forEach((step) => {
      const r = row(p.body, step.text, step.note, 'step-' + step.state);
      const dot = document.createElement('span');
      dot.className = 'sdot';
      r.prepend(dot);
    });
    panels.appendChild(p.sec);
  }

  if (st.jobs && st.jobs.length) {
    const p = panel('Background', String(st.jobs.length));
    st.jobs.forEach((j) => row(p.body, j.goal, j.state + (j.for ? ' · ' + j.for : ''), 'job-' + j.state));
    panels.appendChild(p.sec);
  }

  if (st.reminders && st.reminders.length) {
    const late = st.reminders.filter((r) => r.late).length;
    const p = panel('Reminders', late ? late + ' late' : String(st.reminders.length));
    st.reminders.forEach((r) => row(p.body, r.text, r.due, r.late ? 'late' : ''));
    panels.appendChild(p.sec);
  }

  // Capped. She notices a great deal that is true and not interesting — a repo
  // untouched for a year, every time — and an uncapped list buries the disk
  // filling up under twelve of them. The server sends them most-urgent first.
  if (st.watching && st.watching.length) {
    const p = panel('Noticed', String(st.watching.length));
    st.watching.slice(0, NOTICED_SHOWN).forEach((w) =>
      row(p.body, w.summary, [w.source, w.urgency].filter(Boolean).join(' · ')));
    if (st.watching.length > NOTICED_SHOWN) {
      row(p.body, 'and ' + (st.watching.length - NOTICED_SHOWN) + ' more', '', 'quiet');
    }
    panels.appendChild(p.sec);
  }

  if (st.servers && st.servers.length) {
    const p = panel('Serving', String(st.servers.length));
    st.servers.forEach((sv) => row(p.body, sv.url, sv.dir, sv.alive ? '' : 'dead'));
    panels.appendChild(p.sec);
  }

  if (st.tabs && st.tabs.length) {
    const p = panel('Tabs', String(st.tabs.length));
    st.tabs.forEach((t) => row(p.body, t));
    panels.appendChild(p.sec);
  }

  if (!panels.childElementCount) {
    const quiet = document.createElement('div');
    quiet.className = 'panel-quiet';
    quiet.textContent = st.watchers
      ? 'Nothing on. ' + st.watchers + ' watcher' + (st.watchers === 1 ? '' : 's') + ' running.'
      : 'Nothing on.';
    panels.appendChild(quiet);
  }
}

// Always fetched, even with the panel closed. /state is also how an outstanding
// permission question is recovered when the event stream dropped it, so
// returning early here quietly disabled that whole safety net for anyone who had
// turned the Activity panel off.
async function pollState() {
  clearTimeout(inspectTimer);
  let st = null;
  try {
    const r = await fetch('/state');
    if (r.ok) st = await r.json();
  } catch { /* she may be restarting; the stream indicator already says so */ }
  if (st) renderState(st);
  inspectTimer = setTimeout(pollState, st && st.busy ? INSPECT_BUSY : INSPECT_IDLE);
}

// Below this the CSS hides the inspector outright, so the toggle has nothing to
// toggle and the poll has nobody to render for. Kept in one constant with the
// media query in app.css; they have to agree.
const INSPECT_MIN_WIDTH = 1080;

function inspectorFits() { return window.innerWidth > INSPECT_MIN_WIDTH; }

function showInspector(on) {
  inspectOn = on;
  try { localStorage.setItem('freya.inspect', on ? 'on' : 'off'); } catch {}
  document.body.classList.toggle('no-inspector', !on);
  const btn = $('inspect');
  btn.setAttribute('aria-pressed', String(on));
  // A control that silently does nothing is worse than one that is not there.
  btn.disabled = !inspectorFits();
  btn.title = inspectorFits()
    ? 'What she has on'
    : 'The window is too narrow to show this';
  // The poll is never stopped. It draws the panel only when there is a panel to
  // draw, but /state is also how an outstanding permission question is recovered
  // when the event stream dropped it — so closing the Activity panel must not
  // switch that off.
  pollState();
}

window.addEventListener('resize', () => showInspector(inspectOn));

$('inspect').addEventListener('click', () => showInspector(!inspectOn));

// ---- asking permission, in the thread -------------------------------------
//
// The guard stops before anything destructive and asks. This used to be a modal
// over the whole window; it is a card in the conversation now, where the
// question happened, with its options inline.
//
// The card has to be a child of #thread rather than of the turn, and that is
// forced rather than preferred: a confirm can arrive with no turn running, and
// can be REPLAYED to a window that connects late — so there may be no turn
// element to write into, and on a replay the question legitimately belongs
// mid-scrollback. #thread always exists.
//
// Three decisions are carried over from the terminal prompt, because each of
// them was a decision:
//
//   - The preview is the safety feature. "Delete 4,312 files totalling 8.2 GB"
//     is a decision; "Are you sure?" is a reflex. So the effect sits ABOVE the
//     command and carries the weight.
//   - A destructive action needs the word "yes" typed in full. Muscle memory
//     clicks the primary button before the eyes have finished reading.
//   - Silence is a no, and the countdown says so out loud.
//
// A modal is loud for free, because it disappears. A card lives in the
// scrollback forever, so it has to be SEEN to fall out of the top tier once it
// is answered — hence the deflation, and #pending in the topbar while any card
// is still open.

const perms = new Map();   // id -> {p, card, tick, born, bornInTurn}

function permCount() {
  let n = 0;
  for (const r of perms.values()) if (r.card.dataset.state === 'open') n += 1;
  return n;
}

function syncPending() {
  const n = permCount();
  const btn = $('pending');
  btn.hidden = n === 0;
  btn.textContent = n + ' waiting';
}

function askPermission(raw) {
  let p;
  try { p = JSON.parse(raw); } catch { return; }
  if (!p || !p.id) return;
  // Idempotent across the stream and the poll: the same question arrives down
  // both, and a replay to a reconnecting window arrives again.
  if (perms.has(p.id)) return;

  clearEmpty();
  const card = el('article', 'turn perm');
  card.dataset.state = 'open';
  card.dataset.id = p.id;
  const destructive = p.risk === 'destructive';
  if (destructive) card.dataset.risk = 'destructive';

  const head = el('header', 'perm-head');
  head.append(el('span', 'risk ' + (destructive ? 'high' : 'mid'), p.risk || 'risk'),
    el('span', 'perm-title', 'She wants to do this'));
  const clock = el('span', 'perm-clock');
  head.appendChild(clock);
  card.appendChild(head);

  // Effect first. app.js has said in a comment for weeks that the preview is
  // what is being decided, while the DOM put the command above it.
  if (p.preview) card.appendChild(el('div', 'perm-effect', p.preview));
  card.appendChild(el('pre', 'perm-cmd', p.command || ''));
  if (p.reason) card.appendChild(el('p', 'perm-why', 'She says: ' + p.reason));

  const typed = el('label', 'perm-typed');
  const word = el('input', 'perm-word');
  word.type = 'text';
  word.autocomplete = 'off';
  word.spellcheck = false;
  word.setAttribute('aria-label', 'Type yes to allow');
  typed.append(document.createTextNode('This cannot be undone. Type '),
    el('b', '', 'yes'), document.createTextNode(' to allow it.'), word);
  typed.hidden = !destructive;
  card.appendChild(typed);

  const foot = el('footer', 'perm-foot');
  const no = el('button', 'ghost perm-no', "Don't");
  no.type = 'button';
  const yes = el('button', 'danger perm-yes', destructive ? 'Allow anyway' : 'Allow');
  yes.type = 'button';
  yes.disabled = destructive;
  foot.append(no, yes);
  card.append(foot, el('div', 'perm-verdict'));

  no.addEventListener('click', () => decide(p.id, false));
  yes.addEventListener('click', () => decide(p.id, true));
  word.addEventListener('input', () => {
    yes.disabled = word.value.trim().toLowerCase() !== 'yes';
  });
  word.addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter' && !yes.disabled) { ev.preventDefault(); decide(p.id, true); }
  });

  thread.appendChild(card);
  const rec = { p, card, tick: 0, born: now(), bornInTurn: busy };
  perms.set(p.id, rec);

  let left = p.seconds || 0;
  const show = () => {
    if (left <= 0) { clock.textContent = ''; return; }
    clock.textContent = Math.floor(left / 60) + ':' + String(left % 60).padStart(2, '0') + ' left';
  };
  show();
  rec.tick = setInterval(() => {
    left -= 1;
    show();
    if (left <= 0) clearInterval(rec.tick);
  }, 1000);

  syncPending();
  // A replayed card lands where the scroll already is: yanking the viewport away
  // from someone who is reading is hostile, and a replay is by definition old.
  const live = !p.replayed;
  if (live) {
    card.classList.add('arriving');
    setTimeout(() => card.classList.remove('arriving'), 800);
    scroll(true);
    // Never steal the caret mid-sentence, and never while the recorder is open.
    if (document.activeElement !== input && !listening) {
      (destructive ? word : no).focus();
    }
  }
}

// settlePerm moves a card out of the top tier. Same DOM, one attribute.
function settlePerm(id, state, sentence) {
  const rec = perms.get(id);
  if (!rec || rec.card.dataset.state !== 'open') return;
  clearInterval(rec.tick);
  rec.card.dataset.state = state;
  const cmd = rec.p.command || '';
  const said = sentence || ({
    allowed: 'Allowed',
    refused: 'Declined',
    expired: 'No answer — declined',
  })[state] || state;
  // Expired reads as mute, not as a failure: "I missed one" and "I refused one"
  // are different facts about your own afternoon, and neither is an error.
  rec.card.querySelector('.perm-verdict').textContent = cmd ? said + ' — ' + cmd : said;
  syncPending();
}

async function decide(id, ok) {
  const rec = perms.get(id);
  if (!rec || rec.card.dataset.state !== 'open') return;
  settlePerm(id, ok ? 'allowed' : 'refused');
  try {
    const r = await fetch('/answer', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id, ok }),
    });
    // 410 means the question timed out or was answered elsewhere. Swallowing it
    // made a too-late "Allow" look exactly like an approval.
    if (r.status === 410) {
      rec.card.dataset.state = 'expired';
      rec.card.querySelector('.perm-verdict').textContent =
        'That question had already expired, so it was declined. ' + (rec.p.command || '');
      syncPending();
    } else if (!r.ok) {
      renderNote('That answer was not accepted (' + r.status + ').');
    }
  } catch (err) {
    renderNote('Could not send that answer: ' + String(err));
  }
}

// The state poll settles the other direction: a card no longer outstanding on
// the server was answered somewhere else, or timed out. The age guard matters —
// a poll already in flight when a card is created would otherwise kill a live
// question before the server had ever heard of it.
const PERM_SETTLE_GRACE = 3000;

function reconcileAsks(asking) {
  const live = new Set((asking || []).map((a) => a.id));
  for (const [id, rec] of perms) {
    if (rec.card.dataset.state !== 'open') continue;
    if (live.has(id)) continue;
    if (now() - rec.born < PERM_SETTLE_GRACE) continue;
    settlePerm(id, 'expired');
  }
}

// Clicking the verdict re-reveals what was decided, read-only. The buttons never
// come back.
thread.addEventListener('click', (ev) => {
  const v = ev.target.closest('.perm-verdict');
  if (v) v.closest('.perm')?.classList.toggle('expand');
});

// Escape declines the oldest open card. Anything unparsed is a no, in the window
// as at the prompt.
document.addEventListener('keydown', (ev) => {
  if (ev.key !== 'Escape') return;
  for (const [id, rec] of perms) {
    if (rec.card.dataset.state === 'open') { ev.preventDefault(); decide(id, false); return; }
  }
});

$('pending').addEventListener('click', () => {
  for (const rec of perms.values()) {
    if (rec.card.dataset.state !== 'open') continue;
    rec.card.scrollIntoView({ block: 'center' });
    rec.card.querySelector('.perm-no')?.focus();
    return;
  }
});

function finish() {
  busy = false;
  send.disabled = false;
  // Only if the microphone is actually shut. A turn ending while the recorder is
  // still open — she finishes answering, you tap the mic before the trace
  // settles — used to put the indicator out while the room was still being
  // recorded, which is the one light in this window that must never lie.
  if (!listening) micState('');
  $('stop').hidden = true;
  sealWork();
  // Only the questions THIS turn raised. The modal version declined everything
  // outstanding, which meant a typed turn ending silently refused a background
  // job's question — the exact false refusal the server's replay machinery
  // exists to prevent. A card that arrived with no turn running keeps its own
  // clock and its own five minutes.
  for (const [id, rec] of perms) {
    if (rec.card.dataset.state === 'open' && rec.bornInTurn) decide(id, false);
  }
  endTurn();
  input.focus();
  loadHistory();
  pollState();
}

// ---- sending --------------------------------------------------------------

async function ask(text) {
  busy = true;
  send.disabled = true;
  $('stop').hidden = false;
  userTurn(text);
  beginFreyaTurn();
  pollState();
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
      // A button, not a div. It behaves like one — click it and the conversation
      // opens — and built as a div it was unreachable from the keyboard and
      // announced as nothing by a screen reader.
      const item = document.createElement('button');
      item.type = 'button';
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

// A new conversation cuts the archive here, so the rail gets a new row. It does
// not make her forget: the archive is unbroken and she still knows what you were
// just doing. A button that silently threw that away would be worse.
$('new-chat').addEventListener('click', async () => {
  if (busy) return;
  try {
    const r = await fetch('/session', { method: 'POST' });
    // 409 means a terminal session holds the archive, so there is nothing to cut
    // here. Clearing the thread anyway would show a new conversation that the
    // rail will never have a row for.
    if (r.status === 409) {
      renderStopped('A terminal session has her memory — the conversation was not cut.');
      return;
    }
    if (!r.ok) {
      renderError('could not start a new conversation (' + r.status + ')');
      return;
    }
  } catch (err) {
    renderError('could not start a new conversation: ' + String(err));
    return;
  }
  thread.textContent = '';
  const empty = document.createElement('div');
  empty.className = 'empty';
  const p = document.createElement('p');
  p.textContent = 'New conversation. She still remembers the last one.';
  empty.appendChild(p);
  thread.appendChild(empty);
  turnEl = null;
  traceEl = null;
  $('title').textContent = 'New conversation';
  input.focus();
  loadHistory();
});

try {
  const saved = localStorage.getItem('freya-theme');
  if (saved) document.documentElement.dataset.theme = saved;
} catch {}

connect();
loadHistory();
showInspector(inspectOn);
input.focus();
