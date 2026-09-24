const reduceMotion = matchMedia('(prefers-reduced-motion: reduce)').matches;
const status = document.querySelector('#copy-status');
const wait = ms => new Promise(r => setTimeout(r, ms));
const pick = list => list[Math.floor(Math.random() * list.length)];

/* ---------- Copy ---------- */

function selectText(el) {
  if (!el) return;
  const range = document.createRange();
  range.selectNodeContents(el);
  const selection = window.getSelection();
  selection.removeAllRanges();
  selection.addRange(range);
}

// Commands in a terminal panel, without the explanatory comment lines.
function panelText(panel) {
  return panel.textContent.split('\n')
    .filter(line => !/^\s*(#|\/\/)/.test(line))
    .join('\n').replace(/\n{3,}/g, '\n\n').trim();
}

async function copy(button) {
  let text, source;
  if (button.hasAttribute('data-copy-panel')) {
    source = button.closest('[data-tabs]').querySelector('[role=tabpanel]:not([hidden]) code');
    text = panelText(source);
  } else {
    text = button.dataset.copy;
    source = button.parentElement.querySelector('code');
  }
  const isIcon = button.classList.contains('icon-btn');
  const label = button.textContent;
  try {
    await navigator.clipboard.writeText(text);
    if (!isIcon) button.textContent = 'Copied!';
    button.classList.add('copied');
    status.textContent = 'Copied to clipboard.';
  } catch {
    selectText(source);
    if (!isIcon) button.textContent = 'Selected';
    status.textContent = 'Copy unavailable. The text is selected; press Ctrl+C or Cmd+C to copy.';
  }
  clearTimeout(button._t);
  button._t = setTimeout(() => {
    if (!isIcon) button.textContent = label;
    button.classList.remove('copied');
  }, 1800);
}

document.addEventListener('click', e => {
  const button = e.target.closest('[data-copy], [data-copy-panel]');
  if (button) copy(button);
});

/* ---------- Tabs (roving tabindex, arrow keys) ---------- */

for (const list of document.querySelectorAll('[role=tablist]')) {
  const tabs = [...list.querySelectorAll('[role=tab]')];
  if (!tabs.some(t => t.hasAttribute('aria-controls'))) continue; // levels demo handles itself
  const select = tab => {
    for (const t of tabs) {
      const on = t === tab;
      t.setAttribute('aria-selected', on);
      t.tabIndex = on ? 0 : -1;
      document.getElementById(t.getAttribute('aria-controls')).hidden = !on;
    }
  };
  list.addEventListener('click', e => {
    const tab = e.target.closest('[role=tab]');
    if (tab) select(tab);
  });
  list.addEventListener('keydown', e => {
    const i = tabs.indexOf(document.activeElement);
    if (i < 0) return;
    const next = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 }[e.key];
    if (next === undefined) return;
    e.preventDefault();
    const tab = tabs[(next + tabs.length) % tabs.length];
    tab.focus();
    select(tab);
  });
}

/* ---------- Nav: border on scroll, current section ---------- */

const nav = document.querySelector('.nav');
const onScroll = () => nav.classList.toggle('scrolled', scrollY > 8);
addEventListener('scroll', onScroll, { passive: true });
onScroll();

const navLinks = new Map([...document.querySelectorAll('.nav-links a[href^="#"]')]
  .map(a => [a.getAttribute('href').slice(1), a]));
const spy = new IntersectionObserver(entries => {
  for (const entry of entries) {
    const link = navLinks.get(entry.target.id);
    if (!link) continue;
    if (entry.isIntersecting) link.setAttribute('aria-current', 'true');
    else link.removeAttribute('aria-current');
  }
}, { rootMargin: '-45% 0px -50% 0px' });
for (const id of navLinks.keys()) {
  const section = document.getElementById(id);
  if (section) spy.observe(section);
}

/* ---------- Reveal on scroll ---------- */

const revealed = new IntersectionObserver(entries => {
  for (const entry of entries) {
    if (!entry.isIntersecting) continue;
    entry.target.classList.add('in');
    revealed.unobserve(entry.target);
  }
}, { rootMargin: '0px 0px -8% 0px', threshold: 0.08 });

for (const el of document.querySelectorAll('[data-reveal]')) {
  const siblings = [...el.parentElement.children].filter(c => c.hasAttribute('data-reveal'));
  el.style.setProperty('--delay', `${Math.min(siblings.indexOf(el), 6) * 70}ms`);
  revealed.observe(el);
}

/* ---------- Parallax watermarks ---------- */

const parallax = [...document.querySelectorAll('[data-parallax]')];
if (!reduceMotion && parallax.length) {
  let queued = false;
  const update = () => {
    queued = false;
    for (const el of parallax) {
      const rect = el.parentElement.getBoundingClientRect();
      if (rect.bottom < -200 || rect.top > innerHeight + 200) continue;
      el.style.translate = `0 ${(rect.top * -Number(el.dataset.parallax)).toFixed(1)}px`;
    }
  };
  addEventListener('scroll', () => { if (!queued) { queued = true; requestAnimationFrame(update); } }, { passive: true });
  update();
}

/* ---------- Spotlight cards ---------- */

for (const grid of document.querySelectorAll('[data-spotlight]')) {
  grid.addEventListener('pointermove', e => {
    for (const card of grid.children) {
      const r = card.getBoundingClientRect();
      card.style.setProperty('--mx', `${e.clientX - r.left}px`);
      card.style.setProperty('--my', `${e.clientY - r.top}px`);
    }
  });
}

/* ---------- Gateway diagram: requests flow agent → mcpμ → server and back ---------- */

function gateway(root) {
  const NS = 'http://www.w3.org/2000/svg';
  const svg = root.querySelector('.wires');
  const hub = root.querySelector('.hub');
  const features = Object.fromEntries([...hub.querySelectorAll('[data-f]')].map(f => [f.dataset.f, f]));
  let up = [];
  let down = [];
  let running = false;

  const shown = el => el.offsetParent !== null;
  const rel = (r, box) => ({ l: r.left - box.left, r: r.right - box.left, t: r.top - box.top, b: r.bottom - box.top, cx: r.left - box.left + r.width / 2 });

  function wire(x1, y1, x2, y2) {
    const p = document.createElementNS(NS, 'path');
    const my = (y1 + y2) / 2;
    p.setAttribute('d', `M${x1.toFixed(1)} ${y1.toFixed(1)} C${x1.toFixed(1)} ${my.toFixed(1)} ${x2.toFixed(1)} ${my.toFixed(1)} ${x2.toFixed(1)} ${y2.toFixed(1)}`);
    p.setAttribute('class', 'wire');
    svg.append(p);
    return p;
  }

  function layout() {
    const box = root.getBoundingClientRect();
    svg.setAttribute('viewBox', `0 0 ${box.width} ${box.height}`);
    svg.replaceChildren();
    const h = rel(hub.getBoundingClientRect(), box);
    // Wires land spread across the middle of the hub's edge, in chip order, so none cross.
    const port = (i, n) => h.l + (h.r - h.l) * (n === 1 ? 0.5 : 0.22 + 0.56 * (i / (n - 1)));
    const agents = [...root.querySelectorAll('.agents .chip')].filter(shown);
    const servers = [...root.querySelectorAll('.servers .chip')].filter(shown);
    up = agents.map((chip, i) => {
      const c = rel(chip.getBoundingClientRect(), box);
      return { chip, path: wire(c.cx, c.b, port(i, agents.length), h.t) };
    });
    down = servers.map((chip, i) => {
      const c = rel(chip.getBoundingClientRect(), box);
      return { chip, path: wire(port(i, servers.length), h.b, c.cx, c.t) };
    });
  }

  function travel(path, cls, reverse, duration) {
    const len = path.getTotalLength();
    const seg = Math.min(54, len * 0.4);
    const dot = path.cloneNode();
    dot.setAttribute('class', `packet ${cls}`);
    dot.style.strokeDasharray = `${seg} ${len + seg}`;
    svg.append(dot);
    const frames = [{ strokeDashoffset: seg }, { strokeDashoffset: -len }];
    if (reverse) frames.reverse();
    const anim = dot.animate(frames, { duration, easing: 'cubic-bezier(.5,0,.3,1)' });
    return anim.finished.then(() => dot.remove(), () => dot.remove());
  }

  function flash(el, cls, ms = 1000) {
    el.classList.remove(cls);
    void el.offsetWidth;
    el.classList.add(cls);
    setTimeout(() => el.classList.remove(cls), ms);
  }

  async function roundTrip() {
    if (!up.length || !down.length) return;
    const agent = pick(up);
    const denied = Math.random() < 0.22;
    await travel(agent.path, 'req', false, 850);
    if (denied) {
      flash(hub, 'pulse-deny', 700);
      flash(features.perm, 'on-deny', 900);
      await wait(260);
      await travel(agent.path, 'deny', true, 850);
      flash(agent.chip, 'ping-deny');
      return;
    }
    flash(hub, 'pulse', 600);
    flash(pick([features.ns, features.compress, features.perm, features.metrics].filter(shown)), 'on', 800);
    const server = pick(down);
    await travel(server.path, 'req', false, 750);
    flash(server.chip, 'ping');
    await wait(180);
    await travel(server.path, 'res', true, 750);
    flash(hub, 'pulse', 600);
    await travel(agent.path, 'res', true, 850);
    flash(agent.chip, 'ping');
  }

  // Lanes live forever but park while the diagram is off screen or the tab is hidden.
  const parked = [];
  async function lane(offset) {
    await wait(offset);
    for (;;) {
      if (!running) await new Promise(resolve => parked.push(resolve));
      await roundTrip();
      await wait(300 + Math.random() * 900);
    }
  }

  layout();
  new ResizeObserver(layout).observe(root);
  document.fonts?.ready.then(layout);
  if (reduceMotion) return;

  let inView = false;
  let started = false;
  const update = () => {
    running = inView && !document.hidden;
    if (!running) return;
    parked.splice(0).forEach(resume => resume());
    if (!started) {
      started = true;
      [500, 1600, 2700].forEach(lane);
    }
  };
  new IntersectionObserver(([entry]) => { inView = entry.isIntersecting; update(); }).observe(root);
  document.addEventListener('visibilitychange', update);
}

const gw = document.querySelector('.gateway');
if (gw) gateway(gw);

/* ---------- Compression: 100 schemas → 3 wrappers ---------- */

function contextDemo(root) {
  const cells = root.querySelector('.cells');
  const counter = root.querySelector('[data-count]');
  const buttons = [...root.querySelectorAll('.seg button')];
  const keep = new Set([44, 45, 46]);
  for (let i = 0; i < 100; i++) {
    const cell = document.createElement('i');
    cell.style.setProperty('--d', `${(Math.random() * 0.55).toFixed(2)}s`);
    if (keep.has(i)) cell.className = 'keep';
    cells.append(cell);
  }

  let current = 100;
  let frame;
  function count(to) {
    cancelAnimationFrame(frame);
    const from = current;
    const start = performance.now();
    const duration = reduceMotion ? 0 : 900;
    const step = now => {
      const t = duration ? Math.min(1, (now - start) / duration) : 1;
      current = Math.round(from + (to - from) * (1 - Math.pow(1 - t, 3)));
      counter.textContent = current;
      if (t < 1) frame = requestAnimationFrame(step);
    };
    frame = requestAnimationFrame(step);
  }

  let touched = false;
  function set(mode) {
    root.dataset.mode = mode;
    for (const b of buttons) b.setAttribute('aria-pressed', b.dataset.mode === mode);
    count(mode === 'compressed' ? 3 : 100);
  }
  for (const b of buttons) b.addEventListener('click', () => { touched = true; set(b.dataset.mode); });

  // Play the compression once, when the panel is first properly in view.
  const io = new IntersectionObserver(([entry]) => {
    if (!entry.isIntersecting) return;
    io.disconnect();
    setTimeout(() => { if (!touched) set('compressed'); }, 1100);
  }, { threshold: 0.6 });
  io.observe(root);
}

const ctx = document.querySelector('[data-ctx]');
if (ctx) contextDemo(ctx);

/* ---------- Compression levels: mirrors formatListing in internal/server/compress.go ---------- */

const sampleTools = [
  { name: 'filesystem.read_file', args: ['path', 'limit?'], desc: 'Read a file from disk. Supports partial reads with an optional line limit and returns the text content.' },
  { name: 'github.create_issue', args: ['owner', 'repo', 'title', 'body?'], desc: 'Create an issue in a repository. Returns the issue number and URL so other tools can link to it.' },
  { name: 'docs.search', args: ['query', 'limit?'], desc: 'Search library documentation for relevant sections. Results include source links and are ranked by relevance.' },
  { name: 'jira.get_issue', args: ['key', 'fields?'], desc: 'Fetch a Jira issue by key. Optionally restrict the returned fields to keep responses small.' },
  { name: 'slack.post_message', args: ['channel', 'text', 'thread_ts?'], desc: 'Post a message to a channel or thread. Markdown and mentions are supported.' },
];

function firstSentence(s) {
  const m = s.match(/^.*?\.(?=\s|$)/);
  return m ? m[0] : s;
}

function listingParts(level, tool) {
  const args = level === 'max' ? null : tool.args.join(', ');
  const desc = level === 'low' ? tool.desc : level === 'medium' ? firstSentence(tool.desc) : '';
  return { args, desc };
}

function levelsDemo(root) {
  const tabs = [...root.querySelectorAll('[data-level]')];
  const out = root.querySelector('[data-listing]');
  const size = root.querySelector('[data-size]');
  const bar = root.querySelector('.size-bar');
  const text = level => sampleTools.map(t => {
    const { args, desc } = listingParts(level, t);
    return `<tool>${t.name}${args === null ? '' : `(${args})`}${desc ? `: ${desc}` : ''}</tool>`;
  }).join('\n');
  const full = text('low').length;

  const span = (cls, str) => Object.assign(document.createElement('span'), { className: cls, textContent: str });
  function render(level) {
    out.replaceChildren(...sampleTools.map((t, i) => {
      const { args, desc } = listingParts(level, t);
      const line = document.createElement('span');
      line.className = 'line';
      line.style.animationDelay = `${i * 40}ms`;
      line.append(span('t', '<tool>'), span('n', t.name));
      if (args !== null) line.append(span('p', '('), span('a', args), span('p', ')'));
      if (desc) line.append(span('p', ': '), span('d', desc));
      line.append(span('t', '</tool>'));
      return line;
    }));
    const n = text(level).length;
    size.textContent = n.toLocaleString();
    bar.style.setProperty('--size', `${Math.max(4, (n / full) * 100).toFixed(1)}%`);
    for (const t of tabs) {
      const on = t.dataset.level === level;
      t.setAttribute('aria-selected', on);
      t.tabIndex = on ? 0 : -1;
    }
  }

  root.querySelector('.levels').addEventListener('click', e => {
    const t = e.target.closest('[data-level]');
    if (t) render(t.dataset.level);
  });
  root.querySelector('.levels').addEventListener('keydown', e => {
    const i = tabs.indexOf(document.activeElement);
    const d = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
    if (i < 0 || !d) return;
    e.preventDefault();
    const t = tabs[(i + d + tabs.length) % tabs.length];
    t.focus();
    render(t.dataset.level);
  });
  render('medium');
}

const levels = document.querySelector('.levels-demo');
if (levels) levelsDemo(levels);
