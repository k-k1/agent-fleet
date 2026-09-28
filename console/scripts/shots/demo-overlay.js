// In-page overlay for the demo recording (demo.mjs evaluates this file into the Console's page).
//
// Headless Chromium draws no pointer, and a recording of a UI changing by itself reads as a
// slideshow, so a drawn cursor goes to every control before it is pressed. The captions carry the
// README's four steps, and the phone is the one scene that is not the Console: the permission
// request as the chat bridge posts it to the session's Slack thread.
//
// Everything here is pointer-events: none. The recorder presses controls with real CDP input at
// the coordinates it reads back, so clicks land on the Console underneath, not on the overlay.
(() => {
  if (window.__demo) return;
  const Z = 2147483000;
  // The caption band sits UNDER the Console rather than over it: #root is shortened by this much,
  // so no caption ever hides the control or the row a step is about. demo.mjs adds the same
  // height to the viewport.
  const BAND = 56;
  const css = `
    .dm-cursor { position: fixed; left: 0; top: 0; width: 26px; height: 26px; z-index: ${Z + 5};
      pointer-events: none; transition: transform 700ms cubic-bezier(.45,.05,.25,1), opacity 250ms;
      filter: drop-shadow(0 2px 3px rgba(0,0,0,.55)); }
    .dm-cursor.finger { width: 44px; height: 44px; }
    .dm-ripple { position: fixed; z-index: ${Z + 4}; width: 44px; height: 44px; margin: -22px 0 0 -22px;
      border-radius: 50%; background: rgba(255,255,255,.35); border: 2px solid rgba(255,255,255,.9);
      pointer-events: none; animation: dm-ripple 520ms ease-out forwards; }
    @keyframes dm-ripple { from { transform: scale(.3); opacity: 1 } to { transform: scale(1.4); opacity: 0 } }
    #root { height: calc(100% - ${BAND}px) !important; }
    .dm-caption { position: fixed; left: 0; right: 0; bottom: 0; height: ${BAND}px; z-index: ${Z + 3};
      pointer-events: none; display: flex; align-items: center; justify-content: center; gap: 12px;
      padding: 0 24px; background: #0a0d13; border-top: 1px solid #253043; color: #f4f7fb;
      font: 600 19px/1.3 system-ui, "Noto Sans CJK JP", "Noto Sans JP", sans-serif; }
    .dm-caption .n { flex: none; width: 30px; height: 30px; border-radius: 50%; display: grid;
      place-items: center; background: #3b82f6; color: #fff; font-size: 16px; }
    .dm-caption .n, .dm-caption .t { transition: opacity 200ms; }
    .dm-caption.swap .n, .dm-caption.swap .t { opacity: 0; }
    .dm-veil { position: fixed; inset: 0; z-index: ${Z + 1}; pointer-events: none; opacity: 0;
      background: rgba(4,6,10,.9); transition: opacity 700ms; }
    .dm-veil.on { opacity: 1; }
    .dm-away { position: fixed; left: 50%; top: calc(50% - ${BAND / 2}px); z-index: ${Z + 2}; pointer-events: none;
      transform: translate(-50%, -50%); opacity: 0; transition: opacity 600ms; text-align: center;
      color: #cfd8e6; font: 500 20px/1.5 system-ui, "Noto Sans CJK JP", sans-serif; }
    .dm-away.on { opacity: 1; }
    .dm-away .clock { font: 300 64px/1 system-ui, sans-serif; color: #fff; letter-spacing: 1px; }
    .dm-phone { position: fixed; left: 50%; top: 50%; z-index: ${Z + 2}; pointer-events: none;
      box-sizing: border-box; width: 330px; height: 600px; margin: ${-300 - BAND / 2}px 0 0 -165px; border-radius: 44px;
      background: #0b0b0d;
      padding: 12px; box-shadow: 0 30px 80px rgba(0,0,0,.7), 0 0 0 2px #2a2d33 inset;
      transform: translateY(130%); transition: transform 650ms cubic-bezier(.2,.8,.2,1); }
    .dm-phone.on { transform: translateY(0); }
    .dm-screen { width: 100%; height: 100%; border-radius: 34px; overflow: hidden; background: #fff;
      color: #1d1c1d; font: 15px/1.45 system-ui, "Noto Sans CJK JP", "Noto Sans JP", sans-serif;
      display: flex; flex-direction: column; }
    .dm-status { display: flex; justify-content: space-between; padding: 10px 26px 6px; font-weight: 600;
      font-size: 14px; }
    .dm-bar { display: flex; align-items: center; gap: 10px; padding: 8px 14px 10px;
      border-bottom: 1px solid #e8e8e8; }
    .dm-bar .back { font-size: 22px; color: #1264a3; line-height: 1; }
    .dm-bar .t { font-weight: 800; font-size: 16px; }
    .dm-bar .s { font-size: 12.5px; color: #616061; }
    .dm-msg { display: flex; gap: 10px; padding: 12px 14px 4px; }
    .dm-av { flex: none; width: 36px; height: 36px; border-radius: 8px; background: #1f6feb; color: #fff;
      display: grid; place-items: center; font: 800 14px/1 system-ui, sans-serif; }
    .dm-who { font-weight: 800; font-size: 15px; }
    .dm-who .app { font-weight: 700; font-size: 10px; color: #616061; background: #e8e8e8; border-radius: 3px;
      padding: 1px 4px; margin: 0 6px; vertical-align: 2px; }
    .dm-who .tm { font-weight: 400; font-size: 12px; color: #616061; }
    .dm-body a { color: #1264a3; text-decoration: none; }
    .dm-replies { display: flex; align-items: center; gap: 10px; padding: 10px 14px 0; color: #616061;
      font-size: 13px; }
    .dm-replies::after { content: ""; flex: 1; border-top: 1px solid #e8e8e8; }
    .dm-btns { display: flex; gap: 8px; margin-top: 6px; }
    .dm-btn { border-radius: 6px; padding: 5px 16px; font-weight: 700; font-size: 14px; border: 1px solid #c8c8c8;
      background: #fff; transition: transform 120ms; }
    .dm-btn.primary { background: #007a5a; border-color: #007a5a; color: #fff; }
    .dm-btn.danger { color: #e01e5a; }
    .dm-btn.pressed { transform: scale(.94); filter: brightness(.9); }
    .dm-done { color: #1d1c1d; }
    .dm-compose { margin: auto 12px 14px; border: 1px solid #c8c8c8; border-radius: 10px; padding: 10px 12px;
      color: #868686; font-size: 14px; }
  `;
  const style = document.createElement("style");
  style.textContent = css;
  document.head.appendChild(style);

  const el = (cls, html = "") => {
    const d = document.createElement("div");
    d.className = cls;
    d.innerHTML = html;
    document.body.appendChild(d);
    return d;
  };
  const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);
  const wait = (ms) => new Promise((r) => setTimeout(r, ms));

  const ARROW = `<svg viewBox="0 0 26 26" width="26" height="26"><path d="M3 2 L3 21 L8.2 16.4 L11.6 24 L15 22.5 L11.7 15 L19 15 Z" fill="#fff" stroke="#111" stroke-width="1.5" stroke-linejoin="round"/></svg>`;
  const FINGER = `<svg viewBox="0 0 44 44" width="44" height="44"><circle cx="22" cy="22" r="17" fill="rgba(255,255,255,.35)" stroke="#fff" stroke-width="2.5"/><circle cx="22" cy="22" r="6" fill="#fff"/></svg>`;

  const cursor = el("dm-cursor", ARROW);
  cursor.style.opacity = "0";
  const caption = el("dm-caption", `<span class="n"></span><span class="t"></span>`);
  const veil = el("dm-veil");
  const away = el("dm-away", `<div class="clock"></div><div class="l"></div>`);
  const phone = el("dm-phone", `<div class="dm-screen"></div>`);
  let pos = { x: 640, y: 420 };
  let finger = false;

  const place = (x, y) => {
    // The arrow's tip is its top-left corner; the finger is centred on the point it taps.
    const ox = finger ? 22 : 3;
    const oy = finger ? 22 : 2;
    cursor.style.transform = `translate(${x - ox}px, ${y - oy}px)`;
  };

  window.__demo = {
    // The last visible element matching `sel` whose text matches `pattern` (a RegExp source;
    // "" matches any). Last, because a stacked modal is appended after the one below it.
    find(sel, pattern = "") {
      const re = new RegExp(pattern);
      const hits = [...document.querySelectorAll(sel)].filter(
        (e) => e.getClientRects().length && re.test((e.textContent || "").trim()),
      );
      return hits.at(-1) || null;
    },
    // Moves the drawn cursor and resolves when it has arrived.
    async move(x, y, ms = 700) {
      cursor.style.opacity = "1";
      cursor.style.transitionDuration = `${ms}ms, 250ms`;
      place(x, y);
      pos = { x, y };
      await wait(ms + 30);
    },
    ripple() {
      const r = el("dm-ripple");
      r.style.left = pos.x + "px";
      r.style.top = pos.y + "px";
      setTimeout(() => r.remove(), 600);
    },
    setFinger(on) {
      finger = on;
      cursor.className = "dm-cursor" + (on ? " finger" : "");
      cursor.innerHTML = on ? FINGER : ARROW;
      place(pos.x, pos.y);
    },
    hideCursor() {
      cursor.style.opacity = "0";
    },
    async caption(n, text) {
      caption.classList.add("swap");
      await wait(200);
      caption.querySelector(".n").textContent = String(n);
      caption.querySelector(".t").textContent = text;
      caption.classList.remove("swap");
    },
    veil(on, clock = "", line = "") {
      veil.classList.toggle("on", on);
      away.querySelector(".clock").textContent = clock;
      away.querySelector(".l").textContent = line;
      away.classList.toggle("on", on && !!clock);
    },
    // The Slack thread as the chat bridge fills it (workspace/agent/internal/bridge): the
    // notification text is the thread's first message, the Allow / Deny buttons are the reply
    // under it, and a press rewrites that reply to the feedback line.
    phone(on, t) {
      if (t) {
        phone.querySelector(".dm-screen").innerHTML = `
          <div class="dm-status"><span>${esc(t.time)}</span><span>▂▄▆ ◔</span></div>
          <div class="dm-bar"><span class="back">‹</span><div><div class="t">${esc(t.thread)}</div><div class="s">${esc(t.channel)}</div></div></div>
          <div class="dm-msg"><div class="dm-av">AF</div><div>
            <div class="dm-who">${esc(t.bot)}<span class="app">APP</span><span class="tm">${esc(t.time)}</span></div>
            <div class="dm-body">${esc(t.headline)}<br>${esc(t.session)}<br><a>${esc(t.link)}</a></div>
          </div></div>
          <div class="dm-replies">${esc(t.replies)}</div>
          <div class="dm-msg"><div class="dm-av">AF</div><div>
            <div class="dm-who">${esc(t.bot)}<span class="app">APP</span><span class="tm">${esc(t.time)}</span></div>
            <div class="dm-reply"><div class="dm-btns"><span class="dm-btn primary">${esc(t.allow)}</span><span class="dm-btn danger">${esc(t.deny)}</span></div></div>
          </div></div>
          <div class="dm-compose">${esc(t.compose)}</div>`;
        phone.dataset.done = t.done;
      }
      phone.classList.toggle("on", on);
    },
    phoneAllowRect() {
      const b = phone.querySelector(".dm-btn.primary");
      const r = b.getBoundingClientRect();
      return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
    },
    async phonePress() {
      phone.querySelector(".dm-btn.primary").classList.add("pressed");
      await wait(220);
      phone.querySelector(".dm-reply").innerHTML = `<div class="dm-done">${esc(phone.dataset.done)}</div>`;
    },
  };
})();
