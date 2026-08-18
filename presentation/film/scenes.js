/* The script.
 *
 * Four movements.
 *
 *   I    the ask                one sentence, typed once
 *   II   the errand             nine scenes that are really one continuous shot
 *   III  not a one-off          the same thing, measured, fifteen more times
 *   IV   why you can leave it   memory, presence, and who owns it
 *
 * Movement II is the film. It is one job that cannot be done without every part
 * of her working in sequence: conversation, then the right browser profile, then
 * mail, then a download, then reading a PDF, then a spreadsheet, then writing a
 * Word file, then an upload, then a calendar. The chat and the running list of
 * what she has done stay on screen the whole way through, because the handoff
 * between those things is the point, and a cut between each one would hide it.
 *
 * Every step maps to a tool that exists: browser_profiles, service_open,
 * browser_downloads, file_read, xlsx_write, docx_write, browser_upload. The
 * on-screen world is drawn rather than photographed, but nothing is drawn that
 * the software cannot do.
 */

/* Where the two halves of the composition sit for the whole of movement II. */
const SPINE = { x: 80, y: 96, w: 440, h: 408 };
const LEDGER = { x: 80, y: 528, w: 440, h: 300 };
const WORK = { x: 560, y: 96, w: 960, h: 708 };

const SCENES = [

/* ===== I. the ask ========================================================= */
{
  id: "the ask", dur: 14500,
  build(s) {
    s.fx({ warm: 0.34, dust: 0.6, grain: 0.05 });
    s.clock("08:12");
    s.cursorOff(0);

    const c = s.win({ x: SPINE.x, y: SPINE.y, w: SPINE.w, h: SPINE.h, title: "freya", cls: "chat" });
    s.ref("chat", c);
    c.body.innerHTML = "<div class='prompt'><span class='ps'>&gt;</span>" +
      "<span class='ty'></span><span class='caret'></span></div>";
    s.show(c.win, 300);

    const ask = "supplier sent Q3 over this morning. check it against our sheet, " +
      "put a summary in the shared drive, and book the 4pm call.";
    s.type(1400, c.body.querySelector(".ty"), ask, 32);
    s.at(6400, () => s.said(c, "you", ask));
    s.pulse(6600, true);
  }
},

/* ===== II. the errand ===================================================== */

/* she asks the one thing she cannot safely assume */
{
  id: "one question back", dur: 10500, keep: true,
  build(s) {
    const c = s.get("chat");
    s.at(500, () => s.said(c, "her",
      "Which account? There are five Chrome profiles on here and two of them have mail signed in."));
    s.at(3600, () => s.said(c, "you", "work one"));

    const l = s.win({ x: LEDGER.x, y: LEDGER.y, w: LEDGER.w, h: LEDGER.h,
      title: "what she has done" });
    l.body.style.padding = "0";
    l.body.innerHTML = "<div class='steps'></div>";
    s.ref("steps", l.body.querySelector(".steps"));
    s.show(l.win, 4400);
    s.step(4800, "opening your work profile");
  }
},

/* the right one of five */
{
  id: "the right account", dur: 14000, keep: true,
  build(s) {
    s.work(300, "chrome", "which profile",
      "<div class='profiles'>" + [
        ["A", "Personal", "ada@gmail.com", ""],
        ["A", "Work", "a.lovelace@northgate.co", "mail signed in"],
        ["D", "Design", "design@northgate.co", ""],
        ["C", "Client sandbox", "", "no account"],
        ["S", "School", "a.lovelace@ucl.ac.uk", "mail signed in"],
      ].map(([i, n, e, t], k) =>
        "<div class='prow' data-p='" + k + "'><div class='av'>" + i + "</div>" +
        "<div><div class='nm'>" + n + "</div>" +
        (e ? "<div class='em'>" + e + "</div>" : "") + "</div>" +
        "<div class='tag'>" + t + "</div></div>").join("") + "</div>");

    s.each(1100, ".prow", 240, n => n.classList.add("in"));
    s.clickOn(3600, () => s.q(".prow[data-p='1']"));
    s.at(4100, () => s.q(".prow[data-p='1']").classList.add("pick"));
    s.stepDone(5000);
    s.step(5300, "finding the supplier email");
  }
},

/* mail is a website, so she opens it */
{
  id: "the inbox", dur: 12500, keep: true,
  build(s) {
    const mail = [
      ["Northgate Supplies", "Q3 statement and line detail", "07:41", true],
      ["Rota", "Thursday cover, please confirm", "07:12", false],
      ["Companies House", "Confirmation statement due", "yesterday", false],
      ["Tom Ferreira", "Re: the kitchen fit-out", "yesterday", false],
      ["Northgate Supplies", "Delivery window moved", "Fri", false],
      ["Payroll", "August payslip", "Fri", false],
    ];
    s.work(300, "chrome  ·  work profile", "mail.northgate.co",
      "<div class='mail'><div class='side'>" +
      "<div class='on'>Inbox</div><div>Starred</div><div>Sent</div><div>Archive</div></div>" +
      "<div class='mlist'>" + mail.map(([who, sub, when, un], i) =>
        "<div class='mrow" + (un ? " unread" : "") + "' data-m='" + i + "'>" +
        "<span class='who'>" + who + "</span><span class='sub'>" + sub + "</span>" +
        "<span class='when'>" + when + "</span></div>").join("") +
      "<div class='mview'><h4>Q3 statement and line detail</h4>" +
      "<div class='from'>accounts@northgatesupplies.co.uk · today 07:41</div>" +
      "<p>Morning. Q3 statement attached, plus the line detail as a spreadsheet " +
      "so you can drop it straight into your own workings.</p>" +
      "<p>Anything that looks off, let us know before the end of the week.</p>" +
      "<div class='attach'>" +
      "<div class='att' data-a='0'><span class='ic'>PDF</span>northgate-Q3-statement.pdf</div>" +
      "<div class='att' data-a='1'><span class='ic'>XLSX</span>Q3-line-detail.xlsx</div>" +
      "</div></div></div></div>");

    s.each(1000, ".mrow", 160, n => n.classList.add("in"));
    s.clickOn(3400, () => s.q(".mrow[data-m='0']"));
    s.at(3900, () => s.q(".mrow[data-m='0']").classList.add("hot"));
    s.at(4800, () => s.q(".mview").classList.add("on"));
    s.each(6400, ".att", 340, n => n.classList.add("in"));
    s.stepDone(7600);
    s.step(7900, "downloading both attachments");
  }
},

/* and the files land on your disk, where you can see them */
{
  id: "the files land", dur: 12500, keep: true,
  build(s) {
    s.clickOn(400, () => s.q(".att[data-a='0']"));
    s.at(850, () => s.q(".att[data-a='0']").classList.add("hot"));
    s.clickOn(1400, () => s.q(".att[data-a='1']"));
    s.at(1850, () => s.q(".att[data-a='1']").classList.add("hot"));

    const bar = s.el("div", "dlbar",
      "<div class='dl' data-d='0'><span class='n'>northgate-Q3-statement.pdf</span>" +
      "<span class='track'><i></i></span></div>" +
      "<div class='dl' data-d='1'><span class='n'>Q3-line-detail.xlsx</span>" +
      "<span class='track'><i></i></span></div>" +
      "<div class='dl'><span class='n' style='color:var(--ink-faint)'>saved to ~/Downloads</span></div>");
    s.at(2300, () => { s.get("work").win.appendChild(bar); bar.classList.add("up"); });
    s.at(2600, () => { bar.querySelector("[data-d='0'] i").style.width = "100%"; });
    s.at(3100, () => { bar.querySelector("[data-d='1'] i").style.width = "100%"; });
    s.at(3900, () => bar.querySelector("[data-d='0']").classList.add("done"));
    s.at(4400, () => bar.querySelector("[data-d='1']").classList.add("done"));
    s.at(6600, () => bar.classList.remove("up"));
    // and out of the document, or it sits under every later window in the act
    s.at(7100, () => bar.remove());

    s.stepDone(5400);
    s.step(5700, "reading the statement");
  }
},

/* she reads the PDF rather than guessing what is in it */
{
  id: "reading the statement", dur: 10000, keep: true,
  build(s) {
    const lines = [
      ["NG-3341", "Oak worktop, 3m", "4", "1,240.00"],
      ["NG-3352", "Brass fittings, boxed", "12", "384.00"],
      ["NG-2210", "Delivery, zone 2", "1", "95.00"],
      ["NG-3390", "Oak worktop, 2m", "3", "1,110.00"],
      ["NG-1180", "Sealant, 5L", "6", "162.00"],
      ["NG-4402", "Fitting labour", "1", "820.00"],
    ];
    s.work(300, "reading the file", "northgate-Q3-statement.pdf",
      "<div class='paper'><div class='sheetbar'><b>northgate-Q3-statement.pdf</b>" +
      "<span class='r'>page 1 of 2</span></div><div class='pad'>" +
      "<div class='inv-h'>Northgate Supplies Ltd</div>" +
      "<div class='inv-s'>Statement · quarter three · account NG-77120</div>" +
      "<table class='tbl'><thead><tr class='in'><th>Code</th><th>Item</th>" +
      "<th class='num'>Qty</th><th class='num'>Total</th></tr></thead><tbody>" +
      lines.map(([c, i, q, t]) =>
        "<tr><td>" + c + "</td><td>" + i + "</td><td class='num'>" + q +
        "</td><td class='num'>" + t + "</td></tr>").join("") +
      "</tbody></table></div></div>");

    s.each(1200, ".tbl tbody tr", 280, n => n.classList.add("in"));
    s.stepDone(4400);
    s.step(4700, "comparing it with our sheet");
  }
},

/* the part that is worth something: the two do not agree */
{
  id: "against our sheet", dur: 23000, keep: true,
  build(s) {
    const theirs = [["NG-3341", "4", "1,240.00"], ["NG-3352", "12", "384.00"],
      ["NG-2210", "1", "95.00"], ["NG-3390", "3", "1,110.00"],
      ["NG-1180", "6", "162.00"], ["NG-4402", "1", "820.00"]];
    const ours = [["NG-3341", "4", "1,240.00"], ["NG-3352", "12", "384.00"],
      ["NG-2210", "1", "95.00"], ["NG-3390", "3", "870.00"],
      ["NG-1180", "6", "162.00"], ["NG-4402", "1", "820.00"]];

    s.work(300, "both files at once", "statement.pdf  +  our workings.xlsx",
      "<div style='position:absolute;inset:0;display:grid;grid-template-columns:1fr 1fr'>" +
      "<div style='position:relative;border-right:1px solid #2a2a30'>" +
      "<div class='paper'><div class='sheetbar'><b>what they sent</b></div><div class='pad'>" +
      "<table class='tbl'><thead><tr class='in'><th>Code</th><th class='num'>Qty</th>" +
      "<th class='num'>Total</th></tr></thead><tbody>" +
      theirs.map(([c, q, t], i) => "<tr data-t='" + i + "'><td>" + c + "</td>" +
        "<td class='num'>" + q + "</td><td class='num'>" + t + "</td></tr>").join("") +
      "</tbody></table></div></div></div>" +
      "<div style='position:relative'>" +
      "<div class='paper'><div class='sheetbar'><b>what we agreed</b></div>" +
      "<div class='grid'>" +
      "<div class='r cols in'><span></span><span>A</span><span>B</span><span>C</span><span>D</span></div>" +
      "<div class='r head'><span>1</span><span>Code</span><span>Qty</span><span>Agreed</span><span>Note</span></div>" +
      ours.map(([c, q, t], i) =>
        "<div class='r' data-o='" + i + "'><span>" + (i + 2) + "</span><span>" + c +
        "</span><span class='num'>" + q + "</span><span class='num'>" + t +
        "</span><span></span></div>").join("") +
      "</div></div></div></div>");

    s.each(1000, ".tbl tbody tr", 180, n => n.classList.add("in"));
    s.each(1200, ".grid .r", 180, n => n.classList.add("in"));

    // the row that does not agree, marked on both sides at the same moment
    s.at(6400, () => {
      const a = s.q("[data-t='3']"), b = s.q("[data-o='3']");
      if (a) a.classList.add("flag");
      if (b) { b.classList.add("flag"); b.lastElementChild.textContent = "240.00 out"; }
    });
    s.stepFlag(7800, "line 4 does not match: 240.00 out");
    s.step(9600, "writing the summary");
  }
},

/* she writes the document, and it is laid out rather than dumped */
{
  id: "she writes it up", dur: 20500, keep: true,
  build(s) {
    s.work(300, "docx_write", "Q3-summary.docx",
      "<div class='paper'><div class='sheetbar'><b>Q3-summary.docx</b>" +
      "<span class='r'>Word document</span></div>" +
      "<div class='doc-page' style='top:38px'>" +
      "<div class='brandline'></div>" +
      "<h2>Q3 supplier statement, reconciled</h2>" +
      "<div class='sub'>Northgate Supplies · account NG-77120 · prepared 08:23</div>" +
      "<div class='para'>Six lines on the statement. Five agree with our workings to the " +
      "penny. Invoiced total is 3,811.00 against 3,571.00 agreed.</div>" +
      "<table class='tbl' style='max-width:560px'><thead><tr class='in'><th>Line</th>" +
      "<th>Code</th><th class='num'>Theirs</th><th class='num'>Ours</th></tr></thead><tbody>" +
      [["1", "NG-3341", "1,240.00", "1,240.00", ""], ["3", "NG-2210", "95.00", "95.00", ""],
       ["4", "NG-3390", "1,110.00", "870.00", "flag"], ["6", "NG-4402", "820.00", "820.00", ""]]
        .map(([l, c, a, b, f]) => "<tr class='" + f + "'><td>" + l + "</td><td>" + c +
          "</td><td class='num'>" + a + "</td><td class='num'>" + b + "</td></tr>").join("") +
      "</tbody></table>" +
      "<div class='note'>Line 4 is 240.00 above what we agreed. The quantity matches, so it " +
      "is the unit price. Worth raising on the call.</div>" +
      "</div></div>");

    s.at(1100, () => s.q(".brandline").classList.add("in"));
    s.at(1600, () => s.q(".doc-page h2").classList.add("in"));
    s.at(2200, () => s.q(".doc-page .sub").classList.add("in"));
    s.at(2900, () => s.q(".doc-page .para").classList.add("in"));
    s.each(3700, ".doc-page .tbl tbody tr", 320, n => n.classList.add("in"));
    s.at(5900, () => s.q(".doc-page .note").classList.add("in"));

    s.stepDone(7600);
    s.step(7900, "filing it in the shared drive");
  }
},

/* filed where the team looks */
{
  id: "into the drive", dur: 13000, keep: true,
  build(s) {
    s.work(300, "chrome  ·  work profile", "drive.northgate.co / Finance / Q3",
      "<div class='drive'>" +
      ["Q2-summary.docx", "supplier-terms.pdf", "budget-2026.xlsx", "board-pack.pdf",
       "headcount.xlsx", "insurance.pdf", "Q1-summary.docx"]
        .map(n => "<div class='dfile'><div class='th'></div><div class='nm'>" + n + "</div></div>")
        .join("") +
      "<div class='dfile fresh' data-fresh style='visibility:hidden'>" +
      "<div class='th'></div><div class='nm'>Q3-summary.docx</div></div>" +
      "</div>" +
      "<div class='uploading'><span class='lbl'>uploading Q3-summary.docx</span>" +
      "<span class='track'><i></i></span><span class='pc'>0%</span></div>");

    s.each(900, ".dfile", 120, n => n.classList.add("in"));
    s.at(2500, () => s.q(".uploading").classList.add("in"));
    s.at(2800, () => { s.q(".uploading i").style.width = "100%"; });
    s.count(2800, 0, 100, 1100, v => {
      const n = s.q(".uploading .pc");
      if (n) n.textContent = Math.round(v) + "%";
    });
    s.at(4100, () => {
      const u = s.q(".uploading");
      u.classList.add("done");
      u.querySelector(".lbl").textContent = "uploaded to Finance / Q3";
      const f = s.q("[data-fresh]");
      f.style.visibility = "visible";
      f.classList.add("in");
    });
    s.stepDone(5600);
    s.step(5900, "putting the call in the diary");
  }
},

/* and the call is booked, with the document on it */
{
  id: "the diary", dur: 11500, keep: true,
  build(s) {
    const hours = ["10", "11", "12", "13", "14", "15", "16", "17"];
    const days = ["MON", "TUE", "WED", "THU", "FRI"];
    s.work(300, "chrome  ·  work profile", "calendar.northgate.co",
      "<div class='cal'><div class='hcol'>" +
      hours.map(h => "<div>" + h + ":00</div>").join("") + "</div>" +
      days.map((d, i) =>
        "<div class='day" + (i === 1 ? " today" : "") + "' data-day='" + i + "'>" +
        "<div class='dayname'>" + d + "</div>" +
        hours.map(() => "<div class='slot'></div>").join("") + "</div>").join("") +
      "</div>");

    // the week as it already stands
    s.at(1000, () => {
      s.event(0, 1, 44, "Standup");
      s.event(1, 3, 88, "Fit-out walkthrough");
      s.event(3, 0, 44, "1:1 with Tom");
      s.event(4, 2, 44, "Invoicing");
    });
    // and the one she puts in
    s.at(3400, () => s.event(3, 6, 62, "Northgate Q3 call", "Q3-summary.docx"));
    s.stepDone(5200);
    s.step(5600, "telling you what happened");
  }
},

/* then she says what she did, and what she could not settle */
{
  id: "she reports back", dur: 23000, keep: true,
  build(s) {
    const c = s.get("chat");
    s.at(600, () => s.said(c, "her",
      "Done. Summary is in Finance / Q3, and the call is Thursday at four with the file " +
      "attached to it." +
      "<span class='when'>work profile · <b>11 minutes</b></span>"));
    s.at(5000, () => s.said(c, "her",
      "One thing I could not settle. Line 4 is 240.00 above what we agreed, same quantity, " +
      "so it is the unit price. I flagged it rather than guessing."));
    s.stepDone(8000);
    s.at(9400, () => { const w = s.get("work"); if (w) w.win.classList.add("dim"); });
  }
},

/* ===== III. and it is not a one-off ====================================== */
{
  id: "act three", dur: 5500,
  build(s) {
    s.fx({ warm: 0.4, dust: 0.45, grain: 0.045 });
    s.cursorOff(0);
    s.show(s.add(s.el("div", "actcard",
      "<div class='k'>and none of that was a one-off</div>" +
      "<h2>Every part of it has been measured on its own.</h2>")), 400);
  }
},

{
  id: "fifteen jobs", dur: 22500,
  build(s) {
    s.fx({ warm: 0.35, dust: 0.4, grain: 0.045 });
    s.cursorOff(0);

    const h = s.add(s.el("div", "headline", "Fifteen jobs.<br>Eight real websites."));
    s.show(h, 300);
    const t = s.add(s.el("div", "tally",
      "<div class='n'>0</div><div class='l'>came back with an answer</div>"));
    s.show(t, 700);

    const wall = s.add(s.el("div", "wall"));
    RUN.forEach((r, i) => {
      const tile = s.el("div", "tile",
        "<div class='site'>" + r.site + "</div>" +
        "<div class='task'>" + r.task + "</div>" +
        "<div class='foot'><span class='tick'>&#10003;</span><span>" +
        Math.round(r.secs) + "s</span></div>");
      wall.appendChild(tile);
      s.show(tile, 1300 + i * 400);
      s.at(1300 + i * 400, () => { t.querySelector(".n").textContent = i + 1; });
    });

    s.at(8600, () => { h.innerHTML = "Fifteen jobs.<br>Nothing got stuck."; });
    s.at(11500, () => {
      t.innerHTML = "<div class='n'>15</div><div class='l'>answers &middot; 0 blocked &middot; 0 gave up</div>";
    });
  }
},

{
  id: "the programs", dur: 21500,
  build(s) {
    s.fx({ warm: 0.5, dust: 0.55, grain: 0.05 });

    const ed = s.win({ x: 96, y: 180, w: 520, h: 400, title: "notes", right: "a GTK program" });
    ed.body.style.padding = "0";
    ed.body.innerHTML =
      "<div class='menubar'><span id='m-file'>File</span><span>Edit</span><span>View</span></div>" +
      "<div class='doc'><i></i><i></i><i class='s'></i><i></i><i class='s'></i></div>";
    s.show(ed.win, 200);

    const st = s.win({ x: 660, y: 250, w: 400, h: 300, title: "preferences", right: "a Qt program" });
    st.body.innerHTML =
      "<div class='checkrow'><div class='box' id='b1'></div><span>Open at login</span></div>" +
      "<div class='checkrow'><div class='box'></div><span>Check for updates</span></div>" +
      "<div class='btnrow'><div class='btn'>Cancel</div><div class='btn' id='ok'>Apply</div></div>";
    s.show(st.win, 600);

    const el = s.win({ x: 1100, y: 180, w: 400, h: 300, title: "tracker", right: "a Chrome app" });
    el.body.innerHTML =
      "<div class='field'>Task name</div><div class='field'>Due</div>" +
      "<div class='btnrow'><div class='btn' id='add'>Add task</div></div>";
    s.show(el.win, 1000);

    const file = () => ed.body.querySelector("#m-file");
    s.clickOn(1800, file);
    s.at(2260, () => file().classList.add("hot"));
    const menu = s.add(s.el("div", "menu",
      "<div>New</div><div>Open</div><div id='sa'>Save As</div><div>Print</div>"));
    s.at(2360, () => {
      const p = s.pointOf(file(), 0, 1);
      s.place(menu, p[0] - 14, p[1] + 6, null, null);
      menu.classList.add("on");
    });
    s.clickOn(3000, () => menu.querySelector("#sa"));
    s.at(3400, () => menu.querySelector("#sa").classList.add("hot"));
    s.at(4100, () => { menu.classList.remove("on"); file().classList.remove("hot"); });
    s.stampAt(4400, 120, 620, "The editor said so itself.",
      "menu item 3311 chosen &middot; written to its own log");

    s.clickOn(5400, () => st.body.querySelector("#b1"));
    s.at(5900, () => st.body.querySelector("#b1").classList.add("on"));
    s.clickOn(6600, () => st.body.querySelector("#ok"));
    s.at(7100, () => st.body.querySelector("#ok").classList.add("hot"));
    s.stampAt(7500, 660, 600, "So did the settings window.",
      "Apply pressed &middot; state changed on disk");

    s.clickOn(8400, () => el.body.querySelector(".field"));
    s.at(8880, () => el.body.querySelector(".field").classList.add("hot"));
    s.type(9000, el.body.querySelector(".field"), "call the plumber", 24);
    s.clickOn(10300, () => el.body.querySelector("#add"));
    s.at(10800, () => el.body.querySelector("#add").classList.add("hot"));
    s.stampAt(11200, 1100, 520, "And the Chrome one.",
      "clicked at 640,407 &middot; only after we asked it to speak");
  }
},

/* ===== IV. why you can leave it alone ==================================== */
{
  id: "memory", dur: 11500,
  build(s) {
    s.fx({ warm: 0.42, dust: 0.5, grain: 0.045 });
    s.cursorOff(0);

    s.show(s.add(s.el("div", "headline", "She remembers.")), 300);

    const shelf = s.add(s.el("div", "shelf"));
    [["this morning", "the Q3 statement, one line flagged"],
     ["yesterday", "the invoice went out"],
     ["last week", "we agreed the flight had to be a morning one"],
     ["12 June", "you decided the pricing stays flat at nine a month"],
     ["3 May", "the old build machine finally died"],
     ["March", "the whole plan, before any of it existed"]].forEach(([when, what], i) => {
      const slab = s.el("div", "slab",
        "<span class='when'>" + when + "</span><span>" + what + "</span>");
      slab.style.transform = "scale(" + (1 - i * 0.035) + ") translateY(" + (-i * 5) + "px)";
      slab.style.zIndex = String(20 - i);
      shelf.appendChild(slab);
      s.show(slab, 900 + i * 380);
    });

    const q = s.add(s.el("div", "searchline",
      "<span class='mag'>&#8981;</span><span class='ty'></span><span class='caret'></span>"));
    s.show(q, 4200);
    s.at(4300, () => q.classList.add("hot"));
    s.type(4400, q.querySelector(".ty"), "what did we settle on for pricing", 28);

    s.at(7600, () => {
      const slabs = shelf.querySelectorAll(".slab");
      slabs.forEach((n, i) => { if (i !== 3) n.style.opacity = "0.2"; });
      slabs[3].classList.add("found");
      slabs[3].style.transform = "scale(1.06)";
      slabs[3].style.zIndex = "40";
    });
  }
},

{
  id: "presence", dur: 13500,
  build(s) {
    s.fx({ warm: 0.28, dust: 0.9, grain: 0.06 });
    s.cursorOff(0);
    s.clock("03:12");
    s.pulse(400, true);

    const g1 = s.add(s.el("div", "ghost")); s.place(g1, 200, 200, 560, 380);
    const g2 = s.add(s.el("div", "ghost")); s.place(g2, 420, 420, 640, 300);
    s.show(g1, 200); s.show(g2, 400);

    const toast = s.add(s.el("div", "toast",
      "<div class='who'>FREYA</div>" +
      "<div class='what'>Northgate replied about line 4. They have credited the 240.00. " +
      "Nothing for you to do, but you will want to know before Thursday.</div>"));
    s.show(toast, 2600);
    s.at(7600, () => toast.classList.remove("in"));
  }
},

{
  id: "yours", dur: 15000,
  build(s) {
    s.fx({ warm: 0.55, dust: 0.5, grain: 0.045 });
    s.pulse(0, false);

    s.show(s.add(s.el("div", "fileicon",
      "<div class='sheet'><span>freya</span></div><div class='cap'>one file</div>")), 400);

    const row = s.add(s.el("div", "claimrow"));
    [["No account", "nothing to sign up for"],
     ["No subscription", "nothing to cancel"],
     ["No one else's server", "your files never left the machine"]].forEach(([big, small], i) => {
      const c = s.el("div", "claim",
        "<div class='big'>" + big + "</div><div class='small'>" + small + "</div>");
      row.appendChild(c);
      s.show(c, 2200 + i * 700);
    });

    s.show(s.add(s.el("div", "credit",
      "<b>52,898</b> lines of Go &middot; <b>zero</b> outside libraries &middot; " +
      "<b>726</b> tests passing &middot; written and run on a <b>2014</b> laptop with no graphics card")), 5200);
  }
},

{
  id: "the name", dur: 8500,
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
