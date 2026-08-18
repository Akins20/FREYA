/* The projector.
 *
 * Three jobs. It fits the fixed 1600x900 stage into whatever window is playing
 * it, it runs each scene against a clock, and it composites the effects plate
 * that Go renders in WebAssembly.
 *
 * It plays by itself. That is the default because a film that waits to be
 * advanced is a slide deck, and the first thing anyone did with the old default
 * was sit through one scene wondering why it had stopped.
 *
 *   (no mode)       plays through, with the keyboard hints still on screen
 *   ?mode=film      the same, with every hint hidden. This is what ffmpeg records
 *   ?mode=explore   stops at each scene so a person can step through with arrows
 *
 * # Scenes that keep the set
 *
 * Movement II is nine scenes that have to read as one continuous shot: the chat
 * and the running list of what she has done stay put while the working window
 * changes hands. A scene marked `keep` therefore inherits the set rather than
 * building a new one, and the dissolve is skipped going into it. Cutting between
 * those beats would hide the one thing the movement is about, which is the
 * handoff from each capability to the next.
 */

const W = 1600, H = 900;
const MODE = new URLSearchParams(location.search).get("mode");
const AUTO = MODE !== "explore";     // it plays unless you ask to drive it
const BARE = MODE === "film";        // and it hides its chrome only for capture
if (BARE) document.body.classList.add("film");

const screenEl = document.getElementById("screen");
const setEl = document.getElementById("set");
const cursorEl = document.getElementById("cursor");
const subEl = document.getElementById("sub");
const progressEl = document.getElementById("progress");
const clockEl = document.getElementById("clock");
const pulseEl = document.getElementById("pulse");
const voEl = document.getElementById("vo");

/* ---- fitting ------------------------------------------------------------- */

function fit() {
  // A hidden or zero-sized viewport would scale the whole stage to nothing, and
  // then every measurement taken off it is zero too, which is a confusing way to
  // discover that a window was never shown.
  const k = Math.max(0.05, Math.min(innerWidth / W, innerHeight / H));
  screenEl.style.transform = "translate(-50%,-50%) scale(" + k + ")";
}
addEventListener("resize", fit);
fit();

/* Where a node sits in stage coordinates. Measuring rather than calculating
   means the script never has to know that a title bar is 32 pixels tall, and a
   change to the chrome cannot silently move a click off its target. */
function pointOf(node, fx, fy) {
  const sr = screenEl.getBoundingClientRect();
  const r = node.getBoundingClientRect();
  const k = sr.width / W;
  return [
    (r.left - sr.left + r.width * (fx === undefined ? 0.5 : fx)) / k,
    (r.top - sr.top + r.height * (fy === undefined ? 0.5 : fy)) / k,
  ];
}

/* ---- the stage API the script is written against ------------------------- */

let timers = [];
let refs = {};
let fxState = { grain: 0.05, dust: 0.5, warm: 0.4, wipe: 0, flip: false };

function clearTimers() { timers.forEach(clearTimeout); timers = []; }

const stage = {
  at(ms, fn) { timers.push(setTimeout(fn, ms)); },
  pointOf,

  el(tag, cls, html) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (html !== undefined) n.innerHTML = html;
    return n;
  },

  add(node) { setEl.appendChild(node); return node; },
  q(sel) { return setEl.querySelector(sel); },

  /* Things a later scene in the same movement needs to reach. Cleared on a hard
     cut, kept across a `keep` scene, which is the whole mechanism. */
  ref(name, v) { refs[name] = v; return v; },
  get(name) { return refs[name]; },

  place(node, x, y, w, h) {
    node.style.left = x + "px";
    node.style.top = y + "px";
    if (w) node.style.width = w + "px";
    if (h) node.style.height = h + "px";
    return node;
  },

  show(node, ms) { stage.at(ms || 0, () => node.classList.add("in")); },
  hide(node, ms) { stage.at(ms || 0, () => node.classList.remove("in")); },

  /* stagger over everything matching a selector, in document order */
  each(ms, sel, gap, fn) {
    stage.at(ms, () => {
      setEl.querySelectorAll(sel).forEach((n, i) => setTimeout(() => fn(n), i * gap));
    });
  },

  fx(p) { Object.assign(fxState, p); },
  clock(text) { clockEl.textContent = text; },
  pulse(ms, on) { stage.at(ms || 0, () => pulseEl.classList.toggle("on", on)); },

  win(o) {
    const win = stage.el("div", "win " + (o.cls || ""));
    stage.place(win, o.x, o.y, o.w, o.h);
    const bar = stage.el("div", "bar",
      "<span class='dots'><i></i><i></i><i></i></span>" +
      "<span class='t'>" + (o.title || "") + "</span>" +
      "<span class='right'>" + (o.right || "") + "</span>");
    const body = stage.el("div", "body");
    win.appendChild(bar);
    win.appendChild(body);
    setEl.appendChild(win);
    return { win, bar, body, right: bar.querySelector(".right") };
  },

  /* The working window. Created on first use and then reused for the rest of the
     movement: its contents change hands, the window itself does not, because a
     window that closed and reopened nine times would read as nine jobs. */
  work(ms, title, right, html) {
    stage.at(ms, () => {
      let w = refs.work;
      if (!w) {
        w = stage.win({ x: WORK.x, y: WORK.y, w: WORK.w, h: WORK.h, title, right });
        w.body.style.padding = "0";
        w.body.style.transition = "opacity 260ms var(--ease)";
        refs.work = w;
        w.body.innerHTML = html;
        w.win.classList.add("in");
        return;
      }
      w.body.style.opacity = "0";
      setTimeout(() => {
        w.bar.querySelector(".t").textContent = title;
        w.right.textContent = right;
        w.body.innerHTML = html;
        w.body.style.opacity = "1";
      }, 270);
    });
  },

  /* a line of conversation, in the window it was said in */
  said(c, who, html) {
    const t = c.body.querySelector(".ty");
    if (t) t.textContent = "";
    const m = stage.el("div", "msg " + who, html);
    c.body.insertBefore(m, c.body.querySelector(".prompt"));
    requestAnimationFrame(() => m.classList.add("in"));
    // The window is a fixed height, so the oldest message leaves as a new one
    // arrives, the way a chat scrolls.
    const all = c.body.querySelectorAll(".msg");
    if (all.length > 4) all[0].remove();
  },

  /* the running list of what she has actually done */
  step(ms, text) {
    stage.at(ms, () => {
      const host = refs.steps;
      if (!host) return;
      const n = stage.el("div", "s now", "<span class='m'>&rsaquo;</span><span>" + text + "</span>");
      host.appendChild(n);
      requestAnimationFrame(() => n.classList.add("in"));
    });
  },
  stepDone(ms) {
    stage.at(ms, () => {
      const host = refs.steps;
      if (!host) return;
      const n = host.querySelector(".s.now");
      if (n) { n.classList.remove("now"); n.classList.add("done");
        n.querySelector(".m").innerHTML = "&#10003;"; }
    });
  },
  stepFlag(ms, text) {
    stage.at(ms, () => {
      const host = refs.steps;
      if (!host) return;
      const n = stage.el("div", "s flag", "<span class='m'>!</span><span>" + text + "</span>");
      host.appendChild(n);
      requestAnimationFrame(() => n.classList.add("in"));
    });
  },

  /* one block in the calendar week */
  event(day, hour, height, title, attach) {
    const col = setEl.querySelector("[data-day='" + day + "']");
    if (!col) return;
    const ev = stage.el("div", "ev" + (attach ? " mine" : ""),
      "<b>" + title + "</b>" +
      (attach ? "<span class='paperclip'>" + attach + "</span>" : ""));
    ev.style.top = (28 + hour * 44 + 2) + "px";
    ev.style.height = height + "px";
    col.appendChild(ev);
    requestAnimationFrame(() => ev.classList.add("in"));
  },

  count(ms, from, to, dur, onValue) { stage.at(ms, () => ease(from, to, dur, onValue)); },

  countBar(ms, w, from, to, dur, fmt) {
    stage.at(ms, () => ease(from, to, dur, v => { w.right.textContent = fmt(Math.round(v)); }));
  },

  /* typing, one character at a time, at the speed a person types */
  type(ms, node, text, cps) {
    const per = 1000 / (cps || 26);
    stage.at(ms, () => { node.textContent = ""; });
    for (let i = 0; i < text.length; i++) {
      stage.at(ms + 30 + i * per, () => { node.textContent = text.slice(0, i + 1); });
    }
  },

  cursorTo(ms, x, y) {
    stage.at(ms, () => {
      cursorEl.classList.add("on");
      cursorEl.style.transform = "translate(" + x + "px," + y + "px)";
    });
  },
  cursorOn(ms, get, fx, fy) {
    stage.at(ms, () => {
      const n = get();
      if (!n) return;
      const p = pointOf(n, fx, fy);
      cursorEl.classList.add("on");
      cursorEl.style.transform = "translate(" + p[0] + "px," + p[1] + "px)";
    });
  },
  cursorOff(ms) { stage.at(ms || 0, () => cursorEl.classList.remove("on")); },

  clickAt(ms, x, y) {
    stage.cursorTo(ms, x, y);
    stage.at(ms + 120, () => ripple(x, y));
  },
  clickOn(ms, get, fx, fy) {
    stage.cursorOn(ms, get, fx, fy);
    stage.at(ms + 430, () => {
      const n = get();
      if (!n) return;
      const p = pointOf(n, fx, fy);
      ripple(p[0], p[1]);
    });
  },

  /* what an application said about itself, in its own log */
  stampAt(ms, x, y, text, sub) {
    const s = stage.el("div", "stamp", text + "<b>" + sub + "</b>");
    stage.place(s, x, y, null, null);
    setEl.appendChild(s);
    stage.at(ms, () => s.classList.add("in"));
  },
};

function ripple(x, y) {
  const r = stage.el("div", "ripple");
  r.style.left = x + "px";
  r.style.top = y + "px";
  screenEl.appendChild(r);
  setTimeout(() => r.remove(), 640);
}

/* Figures settle like an instrument coming to rest, never like a scoreboard
   spinning. Ease-out cubic, no overshoot. */
function ease(from, to, dur, onValue) {
  const t0 = performance.now();
  (function frame(now) {
    // Chrome hands the callback the frame's start time, which can precede the
    // performance.now() taken a moment earlier, so p arrives slightly negative
    // on the first frame. Unclamped, that shows as a counter starting below zero.
    const p = Math.min(1, Math.max(0, (now - t0) / dur));
    onValue(from + (to - from) * (1 - Math.pow(1 - p, 3)));
    if (p < 1) requestAnimationFrame(frame);
  })(t0);
}

/* ---- the logo ------------------------------------------------------------ */

/* Drawn from the geometry in brand.js rather than set in a font, and mounted in
   the taskbar so it is present in every frame of the film. */
function logo(el, kind, h, colour) {
  const b = BRAND[kind];
  el.innerHTML =
    '<svg viewBox="-8 -8 ' + (b.w + 16) + ' ' + (b.h + 16) + '" height="' + h + '" ' +
    'fill="' + colour + '">' + b.d + '</svg>';
}

function lockup(el, h) {
  const gap = 46;
  const total = BRAND.mark.w + gap + BRAND.word.w;
  el.innerHTML =
    '<svg viewBox="-8 -8 ' + (total + 16) + ' ' + (BRAND.word.h + 16) + '" height="' + h + '">' +
    '<g fill="' + BRAND.amber + '">' + BRAND.mark.d + '</g>' +
    '<g fill="' + BRAND.ink + '" transform="translate(' + (BRAND.mark.w + gap) + ',0)">' +
    BRAND.word.d + '</g></svg>';
}

lockup(document.getElementById("tblogo"), 15);
stage.logo = logo;
stage.lockup = lockup;

/* ---- narration ----------------------------------------------------------- */

let voice = null;   // filled in once narrate.py has produced a track

fetch("vo/manifest.json")
  .then(r => r.ok ? r.json() : null)
  .then(m => { voice = m; })
  .catch(() => {});

function speak(scene, at, text) {
  stage.at(at, () => {
    subEl.textContent = text;
    subEl.classList.add("on");
    if (voice) {
      const line = voice.lines.find(l => l.scene === scene && l.at === at);
      if (line) { voEl.src = "vo/" + line.file; voEl.currentTime = 0; voEl.play().catch(() => {}); }
    }
  });
  // A subtitle stays up roughly as long as it takes to say, floored so a short
  // line does not blink out.
  const hold = Math.max(2400, text.length * 62);
  stage.at(at + hold, () => subEl.classList.remove("on"));
}

/* ---- the reel ------------------------------------------------------------ */

let current = -1;
let cutting = false;

function playScene(i) {
  if (i < 0 || i >= SCENES.length) return;
  const keep = !!SCENES[i].keep;
  current = i;
  clearTimers();

  if (!keep) {
    setEl.innerHTML = "";
    refs = {};
    cursorEl.classList.remove("on");
    cursorEl.style.transform = "translate(-80px,-80px)";
    clockEl.textContent = "08:12";
  }
  subEl.classList.remove("on");
  progressEl.style.width = ((i + 1) / SCENES.length) * 100 + "%";

  SCENES[i].build(stage);
  NARRATION.filter(n => n.scene === i).forEach(n => speak(i, n.at, n.text));

  if (AUTO) stage.at(SCENES[i].dur, () => advance(i + 1));
}

/* A scene that keeps the set is simply the next beat of the same shot. Anything
   else gets the dissolve. */
function advance(i) {
  if (i >= SCENES.length) return;
  if (SCENES[i].keep) playScene(i);
  else cutTo(i);
}

/* The dissolve is rendered in Go: the picture is eaten by a noise edge, the set
   is rebuilt behind it, and the edge carries on in the same direction to uncover
   the next one. A cross-fade would put both scenes on screen at once, which
   reads as a slideshow. */
function cutTo(i) {
  if (cutting) return;
  if (i < 0 || i >= SCENES.length) { fxState.wipe = 0; return; }
  if (SCENES[i].keep && i === current + 1) { playScene(i); return; }
  cutting = true;
  const COVER = 520, REVEAL = 560;

  fxState.flip = false;
  ease(0, 1, COVER, v => { fxState.wipe = v; });

  setTimeout(() => {
    playScene(i);
    fxState.flip = true;
    fxState.wipe = 1;
    ease(1, 0, REVEAL, v => { fxState.wipe = v; });
    setTimeout(() => { fxState.wipe = 0; cutting = false; }, REVEAL + 30);
  }, COVER + 20);
}

/* ---- the effects plate, rendered in Go ----------------------------------- */

const FX_W = 800, FX_H = 450;
const grainCanvas = document.getElementById("grain");
const cutCanvas = document.getElementById("cut");
grainCanvas.width = FX_W; grainCanvas.height = FX_H;
cutCanvas.width = FX_W; cutCanvas.height = FX_H;
const gctx = grainCanvas.getContext("2d");
const cctx = cutCanvas.getContext("2d");

let grainImage = null, cutImage = null, started = 0;

document.addEventListener("fx-ready", () => {
  fxInit(FX_W, FX_H);
  grainImage = new ImageData(fxPixels, FX_W, FX_H);
  cutImage = new ImageData(fxCut, FX_W, FX_H);
  started = performance.now();
  requestAnimationFrame(render);
});

function render(now) {
  const t = (now - started) / 1000;
  const hasCut = fxFrame(t, fxState.grain, fxState.dust, fxState.wipe, fxState.warm, fxState.flip);
  gctx.putImageData(grainImage, 0, 0);
  if (hasCut) {
    cutCanvas.style.opacity = "1";
    cctx.putImageData(cutImage, 0, 0);
  } else {
    cutCanvas.style.opacity = "0";
  }
  requestAnimationFrame(render);
}

const go = new Go();
WebAssembly.instantiateStreaming(fetch("effects.wasm"), go.importObject)
  .then(r => go.run(r.instance));

/* ---- driving ------------------------------------------------------------- */

addEventListener("keydown", e => {
  if (e.key === "ArrowRight") { e.preventDefault(); advance(current + 1); }
  else if (e.key === "ArrowLeft") { e.preventDefault(); cutTo(current - 1); }
  else if (e.key === " ") { e.preventDefault(); playScene(current); }
  else if (e.key === "f" || e.key === "F") {
    const u = new URL(location.href);
    u.searchParams.set("mode", BARE ? "explore" : "film");
    location.href = u.toString();
  }
});

/* ---- the slate ----------------------------------------------------------- */

/* A screen recorder cannot be started at the same instant as the film, so the
   film announces its own first frame: two frames of white, which the capture
   script finds afterwards and trims to. It is the same reason a film set claps a
   board, and it is only ever shown when the chrome is hidden for capture. */
if (BARE) {
  const slate = document.createElement("div");
  slate.style.cssText = "position:fixed;inset:0;background:#fff;z-index:999";
  document.body.appendChild(slate);
  requestAnimationFrame(() => requestAnimationFrame(() => {
    slate.remove();
    playScene(0);
  }));
} else {
  playScene(0);
}
