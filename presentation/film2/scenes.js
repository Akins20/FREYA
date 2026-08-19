/* Film two: it told me it was done.
 *
 * One continuous shot, about two minutes.
 *
 * # What was wrong with the two cuts before this
 *
 * The first was type on a black screen. The second replaced the type with
 * drawings of windows, which looked better and was still a slide deck, because
 * every beat was a fresh arrangement that faded in, sat still, and cut to a
 * different one. Nothing was ever happening; things had simply already happened.
 *
 * So the desk is never cleared. The chat window is built once and stays for the
 * whole film. The working window beside it is built once and its contents change
 * hands, the way they did through the errand in film one. Every beat is marked
 * keep, there is no dissolve anywhere in the middle, and something is moving in
 * every second of it: text streaming in, ticks landing one at a time, a counter
 * climbing, a cursor crossing to the thing it is about to open.
 *
 * The scene durations are fixed, because the narration was recorded and laid on
 * this clock already. What changed is what happens inside them.
 */

const CHAT = { x: 96, y: 190, w: 470, h: 560 };
const WORK = { x: 610, y: 140, w: 900, h: 620 };

/* Text arriving the way a model actually emits it, a few characters at a time,
   rather than a finished paragraph appearing at once. Most of the motion in the
   film is this. */
function streams(s, at, c, text, cps) {
  s.at(at, () => {
    const m = s.el("div", "msg her", "");
    c.body.insertBefore(m, c.body.querySelector(".prompt"));
    requestAnimationFrame(() => m.classList.add("in"));
    const per = 1000 / (cps || 42);
    for (let i = 0; i < text.length; i++) {
      setTimeout(() => { m.innerHTML = text.slice(0, i + 1); }, 40 + i * per);
    }
    const all = c.body.querySelectorAll(".msg");
    if (all.length > 3) all[0].remove();
  });
}

/* A line of commentary, laid over the desk rather than replacing it. The windows
   stay where they are and dim, so the shot never breaks. */
function over(s, at, html, under) {
  s.at(at, () => {
    document.querySelectorAll("#set .win").forEach(w => w.classList.add("dim"));
    const b = s.el("div", "big over", "<p>" + html + "</p>" +
      (under ? "<div class='under'>" + under + "</div>" : ""));
    s.add(b);
    requestAnimationFrame(() => b.classList.add("in"));
  });
}

function clearOver(s, at) {
  s.at(at, () => {
    document.querySelectorAll("#set .big.over").forEach(n => n.remove());
    document.querySelectorAll("#set .win").forEach(w => w.classList.remove("dim"));
  });
}

const SCENES = [

/* ---- the claim ---------------------------------------------------------- */
{
  id: "it said it was done", dur: 5500,
  build(s) {
    s.fx({ warm: 0.46, dust: 0.6, grain: 0.05 });
    s.clock("23:40");
    s.cursorOff(0);

    const c = s.win({ x: CHAT.x, y: CHAT.y, w: CHAT.w, h: CHAT.h,
                      title: "freya", cls: "chat" });
    c.win.classList.add("persist");
    c.body.innerHTML = "<div class='prompt'><span class='ps'>&gt;</span>" +
      "<span class='ty'></span><span class='caret'></span></div>";
    s.ref("chat", c);
    s.show(c.win, 300);
    s.pulse(400, true);
    streams(s, 1100, c,
      "Self-Quiz Unit 5 for Systems and Application Security is submitted. " +
      "I'm moving on to Unit 6 now.");
  }
},

/* ---- so you go and look ------------------------------------------------- */
{
  id: "it wasn't", dur: 7500, keep: true,
  build(s) {
    const w = s.win({ x: WORK.x, y: WORK.y, w: WORK.w, h: WORK.h,
                      title: "chrome", cls: "browser persist" });
    s.ref("work", w);
    const url = s.el("div", "urlbar",
      "<span class='nav'>&lt; &gt;</span><span class='u'><span class='ty'></span></span>");
    w.win.insertBefore(url, w.body);
    s.ref("url", url.querySelector(".ty"));
    const strip = s.el("div", "doing",
      "<span class='dot'></span><span class='what'></span><span class='el'></span>");
    w.win.appendChild(strip);
    s.ref("doing", strip.querySelector(".what"));
    s.ref("count", strip.querySelector(".el"));

    s.cursorTo(300, 900, 420);
    s.show(w.win, 700);
    s.at(900, () => { s.get("doing").textContent = "the page it says it handed in"; });
    s.type(1100, s.get("url"), "my.university.edu/quiz/unit-5", 34);

    // the page paints in the order a page does: heading, then state, then body
    s.at(3000, () => {
      w.body.innerHTML = "<div class='page on'><div class='article'>" +
        "<h3>Self-Quiz Unit 5</h3>" +
        "<div class='row'></div><div class='hero'></div>" +
        "<div class='para'><i></i><i></i><i class='s'></i></div>" +
        "</div></div>";
    });
    s.at(4100, () => {
      const row = s.q(".article .row");
      if (row) row.innerHTML = "<span class='chip miss'>Not submitted</span>" +
        "<span class='chip'>Due in 2 days</span>";
    });
    s.cursorTo(4300, 1010, 470);
  }
},

{
  id: "you find out on monday", dur: 10000, keep: true,
  build(s) {
    over(s, 600,
      "An assistant that can only talk<br>can only be <span class='accent'>wrong</span>.",
      "One that can actually do things can be wrong, and then tell you it worked.");
    clearOver(s, 8600);
  }
},

/* ---- the same shape, twice more ----------------------------------------- */
{
  id: "eleven of twenty-eight", dur: 10000, keep: true,
  build(s) {
    const c = s.get("chat"), w = s.get("work");
    streams(s, 300, c, "I've audited all 28 projects in your Development folder.");

    s.at(1200, () => {
      s.get("doing").textContent = "the folder it says it went through";
      s.get("url").textContent = "file:///home/ada/Development";
      s.get("count").textContent = "0 of 28 audited";
      w.body.innerHTML = "<div class='drive' style='grid-template-columns:repeat(7,1fr)'></div>";
      const grid = w.body.querySelector(".drive");
      for (let i = 0; i < 28; i++) {
        const f = s.el("div", "dfile");
        f.innerHTML = "<div class='th' style='height:44px'></div>" +
          "<div class='nm' style='text-align:center'>&nbsp;</div>";
        grid.appendChild(f);
      }
    });
    s.each(1600, ".dfile", 60, n => n.classList.add("in"));

    // eleven of them tick, one at a time, and then it simply stops
    for (let i = 0; i < 11; i++) {
      s.at(3600 + i * 330, () => {
        const f = s.q(".dfile:nth-child(" + (i + 1) + ")");
        if (!f) return;
        f.classList.add("fresh");
        f.querySelector(".nm").innerHTML = "&#10003;";
        s.get("count").textContent = (i + 1) + " of 28 audited";
      });
    }
    s.at(7600, () => {
      const c = s.get("count");
      c.textContent = "stopped at 11 of 28";
      c.style.color = "var(--bad)";
    });
  }
},

{
  id: "the file that never changed", dur: 9000, keep: true,
  build(s) {
    const c = s.get("chat"), w = s.get("work");
    streams(s, 300, c,
      "I have updated and reopened the development status report on your screen.");

    s.at(1400, () => {
      s.get("doing").textContent = "the file, before and after";
      s.get("count").textContent = ""; s.get("count").style.color = "";
      s.get("url").textContent = "file:///home/ada/development-status.md";
      w.body.innerHTML = "<div class='twoup' style='position:absolute;left:34px;right:34px;top:40px'></div>";
      const two = w.body.querySelector(".twoup");
      ["before", "after"].forEach((when, i) => {
        two.appendChild(s.el("div", "filecard" + (i ? " same" : ""),
          "<div class='when'>" + when + "</div>" +
          "<div class='nm'>development-status.md</div>" +
          "<div class='f hit'><span class='k'>size</span><span>18,442 bytes</span></div>" +
          "<div class='f hit'><span class='k'>modified</span><span>11:04</span></div>"));
      });
    });
    s.each(1800, ".filecard", 900, n => n.classList.add("in"));
    s.at(4600, () => {
      const st = s.el("div", "stamped", "identical");
      s.place(st, 1010, 520, null, null);
      s.add(st);
      requestAnimationFrame(() => st.classList.add("in"));
    });
  }
},

{
  id: "not lying, exactly", dur: 9500, keep: true,
  build(s) {
    over(s, 600, "It thought the job<br>was <span class='accent'>finished</span>.",
      "The way you do when you have been at something a while.");
    clearOver(s, 8200);
  }
},

/* ---- what stops it now, happening in the same window -------------------- */
{
  id: "it cannot say done", dur: 9500, keep: true,
  build(s) {
    const c = s.get("chat"), w = s.get("work");
    s.at(300, () => {
      s.get("doing").textContent = "the page she just wrote";
      s.get("url").textContent = "localhost:8000/shop.html";
      w.body.innerHTML = "<div class='page on'><div class='paper'><div class='pad linkpage'>" +
        [["Home", "/index.html"], ["Shop", "/shop.html"], ["Product", "/product.html"],
         ["Checkout", "/checkout.html"], ["Contact", "#"]].map(([a, h], i) =>
          "<div class='row" + (i === 4 ? " dead" : "") + "'><span>" + a +
          "</span><span class='href'>" + h + "</span></div>").join("") +
        "</div></div></div>";
    });
    streams(s, 1400, c, "All finished. The site is up on your screen.");
    s.at(5000, () => {
      const m = c.body.querySelector(".msg:last-of-type");
      if (m) { m.style.transition = "opacity 500ms var(--ease)"; m.style.opacity = "0.2"; }
      const stop = s.el("div", "note dead",
        "<b>held</b>one of the links on the page you just wrote still goes nowhere.");
      s.place(stop, CHAT.x, 640, CHAT.w, null);
      s.add(stop);
      requestAnimationFrame(() => stop.classList.add("in"));
    });
  }
},

{
  id: "told plainly", dur: 9000, keep: true,
  build(s) {
    const c = s.get("chat"), w = s.get("work");
    s.at(200, () => { document.querySelectorAll("#set .note").forEach(n => n.remove()); });
    s.at(400, () => {
      s.get("doing").textContent = "fourteen attempts, none of them worked";
      s.get("url").textContent = "my.university.edu/quiz/unit-5";
      w.body.innerHTML = "<div class='page on' style='padding:14px 18px'>" +
        "<div class='trail' style='position:static;box-shadow:none'>" +
        "<div class='rows'></div></div></div>";
      const rows = w.body.querySelector(".rows");
      for (let i = 0; i < 14; i++) {
        rows.appendChild(s.el("div", "t no",
          "<span class='m'>&#215;</span><span>submit</span>" +
          "<span class='note'>no response</span>"));
      }
    });
    s.each(800, ".trail .t", 150, n => n.classList.add("in"));

    s.at(3400, () => {
      const n = s.el("div", "note",
        "<b>put in front of it before it writes a word</b>" +
        "Fourteen attempts, and not one has worked. Do not describe any of it as done.");
      s.place(n, CHAT.x, 640, CHAT.w, null);
      s.add(n);
      requestAnimationFrame(() => n.classList.add("in"));
    });
    streams(s, 5200, s.get("chat"),
      "I could not submit it. The button never responded, and I tried fourteen times.");
  }
},

{
  id: "show the count", dur: 8000, keep: true,
  build(s) {
    const c = s.get("chat");
    s.at(200, () => { document.querySelectorAll("#set .note").forEach(n => n.remove()); });
    s.at(400, () => {
      s.get("doing").textContent = "and the count it has to carry";
      const w = s.get("work");
      w.body.innerHTML = "<div class='page on' style='padding:26px 30px'>" +
        "<div class='measure' style='position:static;top:auto'>" +
        "<div class='row head in'><span class='what'>claimed</span>" +
        "<span class='where'></span><span class='score'>28</span></div>" +
        "<div class='row' data-r='0'><span class='what'>audited</span>" +
        "<span class='where'></span><span class='score'>19</span></div>" +
        "<div class='row best' data-r='1'><span class='what'>skipped, with reasons</span>" +
        "<span class='where'></span><span class='score'>9</span></div></div></div>";
    });
    s.each(1000, ".measure .row", 700, n => n.classList.add("in"));
    streams(s, 2600, c,
      "19 audited, 9 skipped. Four are not projects, three are folders holding " +
      "other projects, and two are worth a look.");
  }
},

/* ---- the one it still misses -------------------------------------------- */
{
  id: "the shop", dur: 12500, keep: true,
  build(s) {
    const c = s.get("chat"), w = s.get("work");
    s.at(300, () => {
      const m = s.el("div", "msg you", "build me a shop, front end and back end");
      c.body.insertBefore(m, c.body.querySelector(".prompt"));
      requestAnimationFrame(() => m.classList.add("in"));
      const all = c.body.querySelectorAll(".msg");
      if (all.length > 3) all[0].remove();
    });

    s.at(1000, () => {
      s.get("doing").textContent = "building it";
      s.get("url").textContent = "localhost:8000";
      w.body.innerHTML =
        "<div class='page on' style='padding:0'><div class='editor' style='height:100%'>" +
        "<div class='body' style='height:100%;display:grid;grid-template-columns:180px 1fr'>" +
        "<div class='files'></div>" +
        "<div class='preview'><div class='shop-top'><span class='brand'>Verdant</span>" +
        "<div class='cart'>cart <b id='cart'>0</b></div></div>" +
        "<div class='shop-grid'></div></div></div></div></div>";
    });
    ["shop.html", "product.html", "checkout.html", "style.css", "app.js"].forEach((f, i) => {
      s.at(1500 + i * 420, () => {
        const files = s.q(".files");
        if (!files) return;
        const n = s.el("div", "f", "<b>+</b>" + f);
        files.appendChild(n);
        requestAnimationFrame(() => n.classList.add("in"));
      });
    });
    [["Fern Bowl", "24.00"], ["Clay Mug", "18.00"], ["Linen Apron", "32.00"],
     ["Oak Board", "46.00"], ["Copper Pot", "88.00"], ["Stone Vase", "29.00"]]
      .forEach((g, i) => {
        s.at(3200 + i * 380, () => {
          const grid = s.q(".shop-grid");
          if (!grid) return;
          const n = s.el("div", "sc",
            "<div class='im'></div><div class='nm'>" + g[0] + "</div>" +
            "<div class='pr'>&pound;" + g[1] + "</div>");
          grid.appendChild(n);
          requestAnimationFrame(() => n.classList.add("in"));
        });
      });
    s.at(5600, () => { const b = s.q("#cart"); if (b) b.textContent = "1"; });
    s.at(6400, () => { const b = s.q("#cart"); if (b) b.textContent = "2"; });

    s.at(8200, () => {
      const p = s.q(".preview");
      if (p) p.classList.add("dim");
      const gap = s.el("div", "gapbar",
        "No back end.<span>and nothing said about it. Every sentence it wrote was true.</span>");
      s.get("work").win.appendChild(gap);
      requestAnimationFrame(() => gap.classList.add("up"));
    });
  }
},

{
  id: "too much, or too little", dur: 7000, keep: true,
  build(s) {
    over(s, 400,
      "Everything above catches it<br>claiming <span class='accent'>too much</span>.",
      "Nothing catches it quietly doing less.");
  }
},

/* ---- and only now does the desk go ------------------------------------- */
{
  id: "the name", dur: 6500,
  build(s) {
    s.fx({ warm: 0.7, dust: 0.35, grain: 0.04 });
    s.cursorOff(0);
    s.pulse(0, false);
    const card = s.add(s.el("div", "wordmark",
      "<div class='logo'></div><p>An assistant that's actually yours.</p>"));
    s.lockup(card.querySelector(".logo"), 132);
    s.show(card, 500);
  }
},

];
