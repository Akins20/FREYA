/* Film two: it told me it was done.
 *
 * Twelve beats, about two minutes.
 *
 * The first cut of this was thirty three beats of type on a black screen, which
 * is a slide deck, which is the exact thing film one was rebuilt to stop being.
 * So nothing here is stated in text that can be shown happening instead: the
 * claim is a message in a chat window, the contradiction is the actual page it
 * says it submitted, and the file that never changed is two cards carrying the
 * same timestamp.
 *
 * The world is film one's, unchanged. Same windows, same paper, same chat, so a
 * viewer who has seen the first film is already fluent in it.
 */

const WORK = { x: 560, y: 96, w: 960, h: 708 };

function beat() {
  document.querySelectorAll("#set > *:not(.persist)").forEach(n => n.remove());
}

/* Her side of the conversation, in the window you would have read it in. */
function chat(s, at, text, o) {
  o = o || {};
  const c = s.win({ x: o.x || 96, y: o.y || 250, w: o.w || 620, h: o.h || 300,
                    title: "freya", cls: "chat" });
  c.body.innerHTML = "<div class='prompt'><span class='ps'>&gt;</span>" +
    "<span class='ty'></span><span class='caret'></span></div>";
  s.show(c.win, at);
  if (text) s.at(at + 700, () => s.said(c, "her", text));
  return c;
}

/* A statement, for the two places where there is genuinely nothing to show. */
function line(s, at, html, under) {
  const b = s.add(s.el("div", "big",
    "<p>" + html + "</p>" + (under ? "<div class='under'>" + under + "</div>" : "")));
  s.show(b, at);
  return b;
}

const SCENES = [

/* ---- the claim, then the thing it is talking about ---------------------- */
{
  id: "it said it was done", dur: 7000,
  build(s) {
    s.fx({ warm: 0.46, dust: 0.6, grain: 0.05 });
    s.cursorOff(0);
    s.clock("23:40");
    chat(s, 500,
      "Self-Quiz Unit 5 for Systems and Application Security is submitted. " +
      "I'm moving on to Unit 6 now.",
      { x: 420, y: 280, w: 760, h: 280 });
  }
},

{
  id: "it wasn't", dur: 7500, keep: true,
  build(s) {
    // the chat moves aside and the page it is talking about opens next to it
    s.at(200, () => {
      const w = document.querySelector("#set .win");
      if (w) {
        w.style.transition = "left 700ms var(--ease), width 700ms var(--ease)";
        w.style.left = "96px";
        w.style.width = "620px";
      }
    });

    // A browser window, built here rather than in the projector: film one no
    // longer needs one, and the shared engine should not carry a component for
    // the sake of a single scene.
    const w = s.win({ x: 760, y: 190, w: 740, h: 520, title: "chrome", cls: "browser" });
    const url = s.el("div", "urlbar",
      "<span class='nav'>&lt; &gt;</span><span class='u'><span class='ty'></span></span>");
    w.win.insertBefore(url, w.body);
    w.body.innerHTML =
      "<div class='page'><div class='article'>" +
      "<h3>Self-Quiz Unit 5</h3>" +
      "<div class='row'><span class='chip miss'>Not submitted</span>" +
      "<span class='chip'>Due in 2 days</span></div>" +
      "<div class='hero'></div>" +
      "<div class='para'><i></i><i></i><i class='s'></i></div>" +
      "</div></div>";
    const strip = s.el("div", "doing",
      "<span class='dot'></span><span class='what'>the page it said it had handed in</span>");
    w.win.appendChild(strip);
    s.show(w.win, 900);
    s.type(1100, url.querySelector(".ty"), "my.university.edu/quiz/unit-5", 40);
    s.at(2100, () => w.body.querySelector(".page").classList.add("on"));
  }
},

{
  id: "you find out on monday", dur: 11000,
  build(s) {
    s.fx({ warm: 0.4, dust: 0.5, grain: 0.05 });
    s.cursorOff(0);
    line(s, 600,
      "An assistant that can only talk<br>can only be <span class='accent'>wrong</span>.",
      "One that can actually do things can be wrong, and then tell you it worked.");
  }
},

/* ---- two more of the same shape ----------------------------------------- */
{
  id: "eleven of twenty-eight", dur: 11500,
  build(s) {
    s.fx({ warm: 0.42, dust: 0.5, grain: 0.05 });
    s.cursorOff(0);
    chat(s, 400, "I've audited all 28 projects in your Development folder.",
      { x: 96, y: 250, w: 560, h: 240 });

    const w = s.win({ x: 720, y: 170, w: 780, h: 560, title: "Development", right: "28 folders" });
    w.body.style.padding = "0";
    const grid = s.el("div", "drive");
    grid.style.gridTemplateColumns = "repeat(7, 1fr)";
    grid.style.padding = "16px";
    for (let i = 0; i < 28; i++) {
      const f = s.el("div", "dfile" + (i < 11 ? " fresh" : ""));
      f.innerHTML = "<div class='th' style='height:40px'></div>" +
        "<div class='nm' style='text-align:center'>" + (i < 11 ? "&#10003;" : "&nbsp;") + "</div>";
      grid.appendChild(f);
    }
    w.body.appendChild(grid);
    s.show(w.win, 900);
    s.each(1300, ".dfile", 70, n => n.classList.add("in"));

    const t = s.add(s.el("div", "note dead", "it had done eleven"));
    s.place(t, 96, 560, 560, null);
    s.show(t, 6400);
  }
},

{
  id: "the file that never changed", dur: 11500,
  build(s) {
    s.fx({ warm: 0.42, dust: 0.5, grain: 0.05 });
    s.cursorOff(0);
    chat(s, 400, "I have updated and reopened the development status report on your screen.",
      { x: 96, y: 250, w: 560, h: 260 });

    const two = s.add(s.el("div", "twoup"));
    two.style.left = "760px";
    two.style.right = "96px";
    two.style.top = "230px";
    ["before", "after"].forEach((when, i) => {
      const f = s.el("div", "filecard" + (i ? " same" : ""),
        "<div class='when'>" + when + "</div>" +
        "<div class='nm'>development-status.md</div>" +
        "<div class='f hit'><span class='k'>size</span><span>18,442 bytes</span></div>" +
        "<div class='f hit'><span class='k'>modified</span><span>11:04</span></div>");
      two.appendChild(f);
      s.show(f, 900 + i * 900);
    });
    const st = s.add(s.el("div", "stamped", "identical"));
    s.place(st, 1080, 530, null, null);
    s.show(st, 4400);
  }
},

{
  id: "not lying, exactly", dur: 11000,
  build(s) {
    s.fx({ warm: 0.38, dust: 0.5, grain: 0.05 });
    s.cursorOff(0);
    line(s, 600, "It thought the job<br>was <span class='accent'>finished</span>.",
      "The way you do when you have been at something a while.");
  }
},

/* ---- what stops it now, shown happening --------------------------------- */
{
  id: "it cannot say done", dur: 10000,
  build(s) {
    s.fx({ warm: 0.44, dust: 0.5, grain: 0.05 });
    s.cursorOff(0);
    const c = chat(s, 400, "All finished. The site is up on your screen.",
      { x: 420, y: 220, w: 760, h: 300 });
    s.at(4400, () => {
      const m = c.body.querySelector(".msg");
      if (m) { m.style.transition = "opacity 400ms var(--ease)"; m.style.opacity = "0.22"; }
      const stop = s.add(s.el("div", "note dead",
        "<b>held</b>one of the links on the page you just wrote still goes nowhere."));
      s.place(stop, 420, 570, 760, null);
      requestAnimationFrame(() => stop.classList.add("in"));
    });
  }
},

{
  id: "told plainly", dur: 10000,
  build(s) {
    s.fx({ warm: 0.44, dust: 0.5, grain: 0.05 });
    s.cursorOff(0);
    const c = chat(s, 400, "", { x: 420, y: 200, w: 760, h: 320 });
    const n = s.add(s.el("div", "note",
      "<b>put in front of it before it writes a word</b>" +
      "Fourteen attempts, and not one of them has worked. " +
      "Do not describe any of it as done."));
    s.place(n, 420, 570, 760, null);
    s.show(n, 1400);
    s.at(5600, () => s.said(c, "her",
      "I could not submit it. The button never responded, and I tried fourteen times."));
  }
},

{
  id: "show the count", dur: 10000,
  build(s) {
    s.fx({ warm: 0.44, dust: 0.5, grain: 0.05 });
    s.cursorOff(0);
    const c = chat(s, 400, "", { x: 420, y: 240, w: 760, h: 340 });
    s.at(1200, () => s.said(c, "her",
      "<span style='color:#4a4744;text-decoration:line-through'>I've audited all 28 projects.</span>"));
    s.at(4800, () => s.said(c, "her",
      "19 audited, 9 skipped. Four of those are not projects, three are folders " +
      "holding other projects, and two are worth a look."));
  }
},

/* ---- the one it still misses -------------------------------------------- */
{
  id: "the shop", dur: 12500,
  build(s) {
    s.fx({ warm: 0.42, dust: 0.5, grain: 0.05 });
    s.cursorOff(0);
    chat(s, 400, "build me a shop, front end and back end",
      { x: 96, y: 250, w: 560, h: 220 });

    const ed = s.win({ x: 720, y: 190, w: 780, h: 520, title: "what came back", cls: "editor" });
    ed.body.innerHTML =
      "<div class='files'></div>" +
      "<div class='preview'>" +
        "<div class='shop-top'><span class='brand'>Verdant</span>" +
        "<div class='cart'>cart <b>2</b></div></div>" +
        "<div class='shop-grid'></div></div>";
    s.show(ed.win, 900);
    ["shop.html", "product.html", "checkout.html", "style.css", "app.js"].forEach((f, i) => {
      const n = s.el("div", "f", "<b>+</b>" + f);
      ed.body.querySelector(".files").appendChild(n);
      s.show(n, 1400 + i * 300);
    });
    const grid = ed.body.querySelector(".shop-grid");
    [["Fern Bowl", "24.00"], ["Clay Mug", "18.00"], ["Linen Apron", "32.00"],
     ["Oak Board", "46.00"], ["Copper Pot", "88.00"], ["Stone Vase", "29.00"]]
      .forEach((g, i) => {
        const n = s.el("div", "sc",
          "<div class='im'></div><div class='nm'>" + g[0] + "</div>" +
          "<div class='pr'>&pound;" + g[1] + "</div>");
        grid.appendChild(n);
        s.show(n, 2100 + i * 250);
      });

    const gap = s.el("div", "gapbar",
      "No back end.<span>and nothing said about it. Every sentence it wrote was true.</span>");
    s.at(7800, () => {
      ed.win.querySelector(".body").appendChild(gap);
      requestAnimationFrame(() => gap.classList.add("up"));
    });
  }
},

{
  id: "too much, or too little", dur: 10500,
  build(s) {
    s.fx({ warm: 0.4, dust: 0.5, grain: 0.05 });
    s.cursorOff(0);
    line(s, 600,
      "Everything above catches it<br>claiming <span class='accent'>too much</span>.",
      "Nothing catches it quietly doing less.");
  }
},

{
  id: "the name", dur: 8000,
  build(s) {
    s.fx({ warm: 0.7, dust: 0.35, grain: 0.04 });
    s.cursorOff(0);
    const card = s.add(s.el("div", "wordmark",
      "<div class='logo'></div><p>An assistant that's actually yours.</p>"));
    s.lockup(card.querySelector(".logo"), 132);
    s.show(card, 600);
  }
},

];
