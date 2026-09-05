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

// Superseded is not failed. Something else took the turn — a spoken request, the
// stop word — and a red failure box would be wrong about what happened.
function renderStopped(text) {
  if (!turnEl) beginFreyaTurn();
  const body = turnEl.querySelector('.body');
  body.querySelector('.working')?.remove();
  const d = document.createElement('div');
  d.className = 'note';
  d.textContent = text || 'Stopped.';
  body.appendChild(d);
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
      case 'thought':  addThought(e.text); break;
      case 'interim':  addThought(e.text); break;
      case 'tool':     addStep(e.name, e.ok); break;
      case 'reply':    renderReply(e.text); break;
      case 'error':    renderError(e.text); break;
      case 'stopped':  renderStopped(e.text); break;
      case 'done':     finish(); break;
      case 'confirm':         askPermission(e.text); break;
      case 'confirm-timeout': closePermission(e.text); break;
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

function micState(state) {
  listening = state === 'listening';
  const mic = $('mic');
  mic.classList.toggle('listening', state === 'listening');
  mic.classList.toggle('speaking', state === 'speaking');
  if (state === 'listening') $('status').textContent = 'listening';
  else if (state === 'speaking') $('status').textContent = 'speaking';
  else if (!busy) $('status').textContent = '';
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
    turnEl.querySelector('.working')?.remove();
    turnEl = null;
    traceEl = null;
  }
  listening = false;
  micState('');
  busy = true;
  send.disabled = true;
  $('stop').hidden = false;
  $('status').textContent = 'working';
  userTurn(text);
  beginFreyaTurn();
  pollState();
}

async function control(path) {
  try {
    const r = await fetch('/voice/' + path, { method: 'POST' });
    if (!r.ok) {
      $('status').textContent = (await r.text()).trim() || 'that did not work';
      setTimeout(() => { if (!busy) $('status').textContent = ''; }, 4000);
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
  $('ins-voice').textContent = st.voice || 'off';
  $('ins-voice').className = 'pill voice-' + (st.voice || 'off');
  // The button follows her, not the other way round: voice can be turned on from
  // the terminal, from a tool call, or by /voice on, and a toggle that only ever
  // reflected its own clicks would be wrong the moment any of those happened.
  setVoiceButton(st.voice && st.voice !== 'off');
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
  if (!permission && st.asking && st.asking.length) {
    askPermission(JSON.stringify(st.asking[0]));
  }

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

async function pollState() {
  clearTimeout(inspectTimer);
  if (!inspectOn || !inspectorFits()) return;
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
  if (on && inspectorFits()) pollState(); else clearTimeout(inspectTimer);
}

window.addEventListener('resize', () => showInspector(inspectOn));

$('inspect').addEventListener('click', () => showInspector(!inspectOn));

// ---- asking permission ----------------------------------------------------
//
// The guard stops before anything destructive and asks. In a terminal that is a
// prompt; here it is this. Three things are carried over from the terminal
// version deliberately, because each of them was a decision:
//
//   - The preview is the safety feature. "Delete 4,312 files totalling 8.2 GB"
//     is a decision; "Are you sure?" is a reflex. So the effect gets the most
//     visual weight, not the buttons.
//   - A destructive action needs the word "yes" typed in full. Muscle memory
//     clicks the primary button before the eyes have finished reading, and that
//     is exactly the moment this exists to catch.
//   - Silence is a no, and the countdown says so out loud. A timeout that looks
//     identical to a refusal is how she learns the window always says no.

let permission = null;   // the question on screen
let permissionTick = 0;  // its countdown timer

function askPermission(raw) {
  let p;
  try { p = JSON.parse(raw); } catch { return; }

  // One at a time. A second question while one is up would replace it and the
  // first would time out unanswered, which is a silent refusal.
  if (permission) return;
  permission = p;

  $('confirm-risk').textContent = p.risk || 'risk';
  $('confirm-risk').className = 'risk ' + (p.risk === 'destructive' ? 'high' : 'mid');
  $('confirm-cmd').textContent = p.command || '';
  const why = $('confirm-why');
  why.textContent = p.reason ? 'She says: ' + p.reason : '';
  why.hidden = !p.reason;
  const effect = $('confirm-effect');
  effect.textContent = p.preview || '';
  effect.hidden = !p.preview;

  const destructive = p.risk === 'destructive';
  const typed = $('confirm-typed');
  const word = $('confirm-word');
  typed.hidden = !destructive;
  word.value = '';
  $('confirm-yes').disabled = destructive;
  $('confirm-yes').textContent = destructive ? 'Allow anyway' : 'Allow';

  $('confirm-veil').hidden = false;
  (destructive ? word : $('confirm-no')).focus();

  let left = p.seconds || 0;
  const clock = $('confirm-clock');
  const show = () => {
    if (left <= 0) { clock.textContent = ''; return; }
    const m = Math.floor(left / 60), sec = String(left % 60).padStart(2, '0');
    clock.textContent = m + ':' + sec + ' left';
  };
  show();
  clearInterval(permissionTick);
  permissionTick = setInterval(() => { left -= 1; show(); if (left <= 0) clearInterval(permissionTick); }, 1000);
}

function closePermission(id) {
  // Guarded by id: a timeout for a question already answered must not tear down
  // the one now on screen.
  if (!permission || (id && id !== permission.id)) return;
  clearInterval(permissionTick);
  permission = null;
  $('confirm-veil').hidden = true;
  input.focus();
}

async function decide(ok) {
  if (!permission) return;
  const id = permission.id;
  closePermission(id);
  try {
    const r = await fetch('/answer', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id, ok }),
    });
    // 410 means the question timed out or was answered elsewhere. Swallowing it
    // made a too-late "Allow" look exactly like an approval, so the user watched
    // for something to happen and nothing did.
    if (r.status === 410) {
      renderStopped('That question had already expired, so it was declined.');
    } else if (!r.ok) {
      renderError('that answer was not accepted (' + r.status + ')');
    }
  } catch (err) {
    renderError('could not send that answer: ' + String(err));
  }
}

$('confirm-no').addEventListener('click', () => decide(false));
$('confirm-yes').addEventListener('click', () => decide(true));
$('confirm-word').addEventListener('input', (ev) => {
  $('confirm-yes').disabled = ev.target.value.trim().toLowerCase() !== 'yes';
});
$('confirm-word').addEventListener('keydown', (ev) => {
  if (ev.key === 'Enter' && !$('confirm-yes').disabled) { ev.preventDefault(); decide(true); }
});
// Escape declines. Anything unparsed is a no, in the window as at the prompt —
// a dialog that treats ambiguity as consent is worse than no dialog, because it
// looks like a safeguard.
document.addEventListener('keydown', (ev) => {
  if (ev.key === 'Escape' && permission) { ev.preventDefault(); decide(false); }
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
  // A turn that ends with a question still on screen would leave the modal there
  // for a turn that is over, and answering it then posts to an id the server has
  // already forgotten. Anything unanswered is declined, which is the direction
  // this whole dialog errs in.
  if (permission) decide(false);
  $('status').textContent = '';
  turnEl?.querySelector('.working')?.remove();
  turnEl = null;
  traceEl = null;
  input.focus();
  loadHistory();
  pollState();
}

// ---- sending --------------------------------------------------------------

async function ask(text) {
  busy = true;
  send.disabled = true;
  $('stop').hidden = false;
  $('status').textContent = 'working';
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
