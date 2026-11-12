// Line charts on a canvas: time on x, value on y, optional min/max band,
// gaps where data is missing, and a hover readout.

import { h } from './dom.js';

const pad = { top: 10, right: 10, bottom: 22, left: 76 };

function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

// niceMax rounds up to 1, 2 or 5 times a power of ten.
function niceMax(v) {
  if (!(v > 0)) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 5, 10]) if (v <= m * p) return m * p;
  return 10 * p;
}

// niceBinary is niceMax in steps of 1024, for byte values, so axis labels
// come out as round KiB or MiB.
function niceBinary(v) {
  if (!(v > 0)) return 1;
  const k = Math.pow(1024, Math.max(0, Math.floor(Math.log(v) / Math.log(1024))));
  return niceMax(v / k) * k;
}

function timeLabel(ms, span) {
  const d = new Date(ms);
  if (span > 2 * 86400e3) return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  if (span <= 15 * 60e3) return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

export class LineChart {
  // opts: height, max (fixed top, e.g. 100 for percent), format(value),
  // bytes (round the axis to binary units), compact (no axes or hover,
  // for small live charts).
  constructor(opts = {}) {
    this.opts = { height: 180, format: v => String(v), ...opts };
    this.canvas = h('canvas', { class: 'chart-canvas', role: 'img' });
    this.legend = h('div', { class: 'legend' });
    this.el = h('div', { class: 'chart' + (this.opts.compact ? ' compact' : '') }, this.canvas, this.opts.compact ? null : this.legend);
    this.series = [];
    this.hover = null;
    if (!this.opts.compact) {
      this.canvas.addEventListener('mousemove', e => {
        this.hover = e.offsetX;
        this.draw();
      });
      this.canvas.addEventListener('mouseleave', () => {
        this.hover = null;
        this.draw();
      });
    }
    new ResizeObserver(() => this.draw()).observe(this.el);
  }

  // set takes series [{name, color: index, points: [{t, v, lo, hi}]}]
  // with t in milliseconds, and the visible range {from, to, step} (ms).
  set(series, range) {
    this.series = series;
    this.range = range;
    const label = series.map(s => s.name).join(', ');
    this.canvas.setAttribute('aria-label', (this.opts.title ? this.opts.title + ': ' : '') + label);
    this.legend.replaceChildren(...series.map(s => {
      const last = [...s.points].reverse().find(p => p.v !== null && isFinite(p.v));
      return h('span', { class: 'legend-item' },
        h('span', { class: 'swatch s' + (s.color % 6) }), s.name, ' ',
        h('b', null, last ? this.opts.format(last.v) : '—'));
    }));
    this.draw();
  }

  draw() {
    const w = this.el.clientWidth;
    const hgt = this.opts.height;
    if (!w || !this.range) return;
    const dpr = window.devicePixelRatio || 1;
    this.canvas.width = Math.round(w * dpr);
    this.canvas.height = Math.round(hgt * dpr);
    this.canvas.style.height = hgt + 'px';
    const ctx = this.canvas.getContext('2d');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, hgt);

    const p = this.opts.compact ? { top: 2, right: 0, bottom: 2, left: 0 } : pad;
    const { from, to, step } = this.range;
    let top = this.opts.max;
    if (!top) {
      let m = 0;
      for (const s of this.series) for (const pt of s.points) m = Math.max(m, pt.hi ?? pt.v ?? 0);
      top = this.opts.bytes ? niceBinary(m * 1.1) : niceMax(m * 1.1);
    }
    const x0 = p.left;
    const x1 = w - p.right;
    const y0 = hgt - p.bottom;
    const y1 = p.top;
    const X = t => x0 + ((t - from) / (to - from)) * (x1 - x0);
    const Y = v => y0 - (Math.max(0, Math.min(top, v)) / top) * (y0 - y1);
    const grid = cssVar('--grid');
    const muted = cssVar('--muted');
    const font = '11px ' + cssVar('--mono');

    if (!this.opts.compact) {
      ctx.font = font;
      ctx.strokeStyle = grid;
      ctx.fillStyle = muted;
      ctx.lineWidth = 1;
      ctx.textAlign = 'right';
      ctx.textBaseline = 'middle';
      for (let i = 0; i <= 4; i++) {
        const v = (top * i) / 4;
        const y = Math.round(Y(v)) + 0.5;
        ctx.beginPath();
        ctx.moveTo(x0, y);
        ctx.lineTo(x1, y);
        ctx.stroke();
        ctx.fillText(this.opts.format(v), x0 - 6, y);
      }
      ctx.textAlign = 'center';
      ctx.textBaseline = 'top';
      const ticks = Math.max(2, Math.min(6, Math.floor((x1 - x0) / (to - from <= 15 * 60e3 ? 160 : 110))));
      for (let i = 0; i <= ticks; i++) {
        const t = from + ((to - from) * i) / ticks;
        const x = X(t);
        ctx.textAlign = i === 0 ? 'left' : i === ticks ? 'right' : 'center';
        ctx.fillText(timeLabel(t, to - from), x, y0 + 6);
      }
    }

    const gap = (step || 2000) * 2.5;
    this.series.forEach(s => {
      const color = cssVar('--series-' + (s.color % 6));
      const pts = s.points.filter(pt => pt.t >= from - gap && pt.t <= to);
      // Split into runs at gaps and missing values.
      const runs = [];
      let run = [];
      let prev = null;
      for (const pt of pts) {
        if (pt.v === null || !isFinite(pt.v) || (prev && pt.t - prev.t > gap)) {
          if (run.length) runs.push(run);
          run = [];
        }
        if (pt.v !== null && isFinite(pt.v)) run.push(pt);
        prev = pt;
      }
      if (run.length) runs.push(run);
      for (const r of runs) {
        if (r[0].lo !== undefined) {
          ctx.globalAlpha = 0.18;
          ctx.fillStyle = color;
          ctx.beginPath();
          r.forEach((pt, i) => (i ? ctx.lineTo(X(pt.t), Y(pt.hi)) : ctx.moveTo(X(pt.t), Y(pt.hi))));
          for (let i = r.length - 1; i >= 0; i--) ctx.lineTo(X(r[i].t), Y(r[i].lo));
          ctx.closePath();
          ctx.fill();
        } else if (this.series.length === 1) {
          ctx.globalAlpha = 0.12;
          ctx.fillStyle = color;
          ctx.beginPath();
          ctx.moveTo(X(r[0].t), y0);
          for (const pt of r) ctx.lineTo(X(pt.t), Y(pt.v));
          ctx.lineTo(X(r[r.length - 1].t), y0);
          ctx.closePath();
          ctx.fill();
        }
        ctx.globalAlpha = 1;
        ctx.strokeStyle = color;
        ctx.lineWidth = 1.6;
        ctx.lineJoin = 'round';
        ctx.beginPath();
        r.forEach((pt, i) => (i ? ctx.lineTo(X(pt.t), Y(pt.v)) : ctx.moveTo(X(pt.t), Y(pt.v))));
        if (r.length === 1) ctx.arc(X(r[0].t), Y(r[0].v), 1.5, 0, 2 * Math.PI);
        ctx.stroke();
      }
    });

    if (this.hover !== null && this.hover >= x0 && this.hover <= x1) this.drawHover(ctx, X, Y, x0, x1, y0, y1, font);
  }

  drawHover(ctx, X, Y, x0, x1, y0, y1, font) {
    const { from, to } = this.range;
    const t = from + ((this.hover - x0) / (x1 - x0)) * (to - from);
    const rows = [];
    let tx = null;
    for (const s of this.series) {
      let best = null;
      for (const pt of s.points) if (pt.v !== null && (!best || Math.abs(pt.t - t) < Math.abs(best.t - t))) best = pt;
      if (!best || Math.abs(X(best.t) - this.hover) > 24) continue;
      tx = tx ?? best.t;
      rows.push({ s, pt: best });
    }
    if (!rows.length) return;
    const x = Math.round(X(tx)) + 0.5;
    ctx.strokeStyle = cssVar('--muted');
    ctx.globalAlpha = 0.6;
    ctx.beginPath();
    ctx.moveTo(x, y1);
    ctx.lineTo(x, y0);
    ctx.stroke();
    ctx.globalAlpha = 1;
    for (const { s, pt } of rows) {
      ctx.fillStyle = cssVar('--series-' + (s.color % 6));
      ctx.beginPath();
      ctx.arc(X(pt.t), Y(pt.v), 3, 0, 2 * Math.PI);
      ctx.fill();
    }
    const lines = [new Date(tx).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })]
      .concat(rows.map(({ s, pt }) => `${s.name}  ${this.opts.format(pt.v)}` + (pt.lo !== undefined && pt.hi !== pt.lo ? `  (${this.opts.format(pt.lo)}–${this.opts.format(pt.hi)})` : '')));
    ctx.font = font;
    const bw = Math.max(...lines.map(l => ctx.measureText(l).width)) + 16;
    const bh = lines.length * 16 + 8;
    let bx = x + 10;
    if (bx + bw > x1) bx = x - 10 - bw;
    const by = y1 + 4;
    ctx.fillStyle = cssVar('--panel-hi');
    ctx.strokeStyle = cssVar('--border-hi');
    ctx.beginPath();
    ctx.roundRect(bx, by, bw, bh, 4);
    ctx.fill();
    ctx.stroke();
    ctx.textAlign = 'left';
    ctx.textBaseline = 'top';
    lines.forEach((l, i) => {
      ctx.fillStyle = i === 0 ? cssVar('--muted') : cssVar('--text');
      ctx.fillText(l, bx + 8, by + 6 + i * 16);
    });
  }
}
