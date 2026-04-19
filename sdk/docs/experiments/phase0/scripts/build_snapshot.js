// Phase 0 snapshot extractor — mirrors sdk/docs/39 §3.1 data shape.
// Injected into page context by capture.py to produce samples/*/snapshot.json.

(function () {
  const SEL = 'a,button,input,select,textarea,[role=button],[role=link],[role=tab],[role=menuitem],[onclick],[tabindex]:not([tabindex="-1"])';

  function isVisible(el) {
    const r = el.getBoundingClientRect();
    const s = getComputedStyle(el);
    return r.width > 0 && r.height > 0
      && s.visibility !== 'hidden'
      && s.display !== 'none'
      && +s.opacity > 0;
  }

  function textOf(el) {
    const aria = el.getAttribute('aria-label');
    if (aria) return aria.trim();
    const text = (el.innerText || '').trim();
    if (text) return text;
    return (el.placeholder || el.value || el.name || el.title || '').trim();
  }

  window.__brainSnapshot = function () {
    let n = 0;
    const out = [];
    document.querySelectorAll(SEL).forEach((el) => {
      if (!isVisible(el)) return;
      const id = ++n;
      el.setAttribute('data-brain-id', String(id));
      const r = el.getBoundingClientRect();
      out.push({
        id,
        tag: el.tagName.toLowerCase(),
        role: el.getAttribute('role') || el.tagName.toLowerCase(),
        type: el.getAttribute('type') || null,
        name: textOf(el).slice(0, 120),
        value: el.value || null,
        href: el.tagName === 'A' ? (el.href || null) : null,
        x: Math.round(r.x + r.width / 2),
        y: Math.round(r.y + r.height / 2),
        w: Math.round(r.width),
        h: Math.round(r.height),
        inViewport: r.top >= 0 && r.bottom <= innerHeight,
      });
    });
    return out;
  };

  return window.__brainSnapshot();
})();
