// What ouro runs in a page, as one object: browser.go calls one of its
// functions at a time. Each returns an object, with "error" set when it could
// not do what it was asked.
({
  // read returns the page as text, with every element one can act on in it
  // as [n] and what it is, and keeps those elements for the functions below.
  read() {
    const elements = [];
    const roles = new Set(['button', 'link', 'checkbox', 'radio', 'tab', 'menuitem', 'menuitemcheckbox',
      'menuitemradio', 'option', 'switch', 'combobox', 'textbox', 'searchbox', 'slider', 'spinbutton', 'treeitem']);
    const skipped = new Set(['script', 'style', 'noscript', 'template', 'svg', 'canvas', 'head', 'iframe', 'object', 'video', 'audio']);
    const blocks = new Set(['address', 'article', 'aside', 'blockquote', 'dd', 'details', 'dialog', 'div', 'dl', 'dt',
      'fieldset', 'figcaption', 'figure', 'footer', 'form', 'header', 'hr', 'main', 'nav', 'ol', 'p', 'pre', 'section',
      'table', 'tbody', 'thead', 'tfoot', 'tr', 'ul', 'legend', 'caption']);
    let out = '';
    const actable = el => {
      const name = el.localName;
      if (name === 'a') return el.hasAttribute('href');
      if (name === 'input') return el.type !== 'hidden';
      if (['button', 'select', 'textarea', 'summary'].includes(name)) return true;
      if (roles.has(el.getAttribute('role'))) return true;
      if (el.isContentEditable) return !(el.parentElement && el.parentElement.isContentEditable);
      return el.hasAttribute('onclick');
    };
    // A picture is an image that is shown larger than an icon.
    const picture = img => {
      const r = img.getBoundingClientRect();
      // One that did not load is none; one that still loads is.
      return r.width >= 24 && r.height >= 24 && (img.naturalWidth > 0 || !img.complete) && shown(img);
    };
    const shown = el => {
      if (!el.checkVisibility || el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })) return true;
      // An element of display: contents has no box of its own, its children do.
      return getComputedStyle(el).display === 'contents';
    };
    const walk = node => {
      const children = node.localName === 'slot' && node.assignedNodes().length ? node.assignedNodes()
        : (node.shadowRoot || node).childNodes;
      for (const child of children) {
        if (child.nodeType === Node.TEXT_NODE) {
          out += child.data.replace(/\s+/g, ' ');
          continue;
        }
        if (child.nodeType !== Node.ELEMENT_NODE || skipped.has(child.localName) || !shown(child)) continue;
        if (actable(child)) {
          out += ' [' + elements.push(child) + '] ' + this.describe(child) + ' ';
          // The pictures in it, like the one a link leads to, are of their own.
          for (const img of child.querySelectorAll('img')) {
            if (picture(img)) out += ' [' + elements.push(img) + '] ' + this.describe(img) + ' ';
          }
          continue;
        }
        const name = child.localName;
        if (name === 'br') { out += '\n'; continue; }
        if (name === 'img') {
          const alt = this.clean(child.alt);
          // An icon, or a picture that did not load, is only what it says.
          if (picture(child)) out += ' [' + elements.push(child) + '] ' + this.describe(child) + ' ';
          else if (alt) out += ' [image "' + this.cut(alt, 100) + '"] ';
          continue;
        }
        const heading = /^h([1-6])$/.exec(name);
        if (heading) out += '\n' + '#'.repeat(+heading[1]) + ' ';
        else if (name === 'li') out += '\n- ';
        else if (name === 'td' || name === 'th') out += ' | ';
        else if (blocks.has(name)) out += '\n';
        walk(child);
        if (heading || blocks.has(name)) out += '\n';
      }
    };
    walk(document.body || document.documentElement);
    window.__ouroElements = elements;
    const text = out.split('\n').map(line => line.replace(/ +/g, ' ').trim())
      .join('\n').replace(/\n{3,}/g, '\n\n').trim();
    return { title: document.title, url: location.href, text, elements: elements.length };
  },

  // describe says what the element el is, in a line.
  describe(el) {
    const name = el.localName, role = el.getAttribute('role');
    const text = this.cut(this.clean(el.innerText), 150);
    const label = this.cut(this.label(el), 150);
    const value = v => v ? ' = "' + this.cut(this.clean(v), 100) + '"' : '';
    if (name === 'img') {
      const src = el.currentSrc || el.src;
      return 'image "' + this.cut(this.clean(el.alt), 100) + '" ' + el.naturalWidth + '×' + el.naturalHeight +
        (src.startsWith('data:') ? ' (in the page)' : ' → ' + this.cut(src, 120));
    }
    if (name === 'a') {
      const href = el.getAttribute('href');
      return 'link "' + (text || label) + '"' + (href.startsWith('javascript:') ? '' : ' → ' + this.cut(href, 120));
    }
    if (name === 'input') {
      const type = el.type;
      if (type === 'checkbox' || type === 'radio') return type + ' "' + label + '" ' + (el.checked ? 'checked' : 'not checked');
      if (['submit', 'button', 'reset', 'image'].includes(type)) return 'button "' + (el.value || label) + '"';
      const shown = type === 'password' && el.value ? '•••' : el.value;
      return 'input' + (type === 'text' ? '' : ' ' + type) + ' "' + (label || el.placeholder || el.name) + '"' + value(shown);
    }
    if (name === 'textarea') return 'text area "' + (label || el.placeholder || el.name) + '"' + value(el.value);
    if (name === 'select') {
      const options = Array.from(el.options).map(o => this.clean(o.text));
      const chosen = el.selectedIndex >= 0 ? options[el.selectedIndex] : '';
      return 'select "' + (label || el.name) + '"' + value(chosen) + ' (options: ' +
        this.cut(options.join(' | '), 300) + ')';
    }
    if (el.isContentEditable) return 'editable "' + label + '"' + value(el.innerText);
    if (name === 'button' || name === 'summary') return 'button "' + (text || label) + '"';
    return (role || name) + ' "' + (text || label) + '"';
  },

  // label is what names the element el besides its text.
  label(el) {
    const by = el.getAttribute('aria-labelledby');
    const named = by && by.split(/\s+/).map(id => document.getElementById(id)).filter(Boolean)
      .map(e => e.innerText).join(' ');
    const labels = el.labels ? Array.from(el.labels).map(l => l.innerText).join(' ') : '';
    // A link or a button that is only a picture is named by the picture.
    const picture = el.querySelector('img[alt]');
    return this.clean(el.getAttribute('aria-label') || named || labels || el.getAttribute('title') ||
      el.getAttribute('alt') || (picture && picture.alt) || '');
  },

  clean(s) { return (s || '').replace(/\s+/g, ' ').trim(); },
  cut(s, n) { return s.length > n ? s.slice(0, n - 1) + '…' : s; },

  // element is the element [n] of the last read.
  element(n) {
    const el = (window.__ouroElements || [])[n - 1];
    if (!el || !el.isConnected) return null;
    return el;
  },

  // address is where the link or the image [n] leads, for a tab to open.
  address(n) {
    const el = this.element(n);
    if (!el) return { error: 'there is no element [' + n + '] on the page now: read it again' };
    const address = el.localName === 'img' ? el.currentSrc || el.src : el.localName === 'a' ? el.href : '';
    if (!address) return { error: '[' + n + '], ' + this.describe(el) + ', is no link or image: click it instead' };
    if (address.startsWith('data:')) return { error: 'the image [' + n + '] is part of the page, with no address of its own: see it with screenshot' };
    return { address };
  },

  // point brings the element [n] into view and returns the middle of it,
  // where a click goes, and what is on top of it if that is something else.
  point(n) {
    const el = this.element(n);
    if (!el) return { error: 'there is no element [' + n + '] on the page now: read it again' };
    el.scrollIntoView({ block: 'center', inline: 'center' });
    const r = el.getBoundingClientRect();
    const x = r.left + r.width / 2, y = r.top + r.height / 2;
    const hit = document.elementFromPoint(x, y);
    const covered = hit && hit !== el && !el.contains(hit) && !hit.contains(el) &&
      !(hit.shadowRoot && hit.shadowRoot.contains(el)) && !(el.getRootNode().host === hit);
    return { x, y, what: this.describe(el), covered: covered ? this.cut(this.clean(hit.innerText), 80) || hit.localName : '' };
  },

  // click clicks the element [n] the way a script does, for one that is
  // covered by another.
  click(n) {
    const el = this.element(n);
    if (!el) return { error: 'there is no element [' + n + '] on the page now: read it again' };
    el.click();
    return {};
  },

  // focus readies the element [n] for typing: it focuses it and selects
  // what it holds, for what is typed to replace it. A select takes text as
  // the option to choose right here.
  focus(n, text) {
    const el = this.element(n);
    if (!el) return { error: 'there is no element [' + n + '] on the page now: read it again' };
    const what = this.describe(el);
    if (el.localName === 'select') {
      const want = this.clean(text).toLowerCase();
      const option = Array.from(el.options).find(o => this.clean(o.text).toLowerCase() === want || o.value === text);
      if (!option) return { error: '[' + n + '] has no option "' + text + '": ' + what };
      el.value = option.value;
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));
      return { what, chosen: true };
    }
    const typed = el.localName === 'textarea' || el.isContentEditable ||
      (el.localName === 'input' && !['checkbox', 'radio', 'submit', 'button', 'reset', 'image', 'file'].includes(el.type));
    if (!typed) return { error: 'nothing can be typed into [' + n + '], ' + what + ': click it instead' };
    el.scrollIntoView({ block: 'center' });
    el.focus();
    if (el.isContentEditable) {
      const range = document.createRange();
      range.selectNodeContents(el);
      const selection = getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
    } else {
      el.select();
    }
    return { what };
  },

  // scroll scrolls the page by most of a screen, down or up (by -1), and
  // says where it is then.
  scroll(by) {
    window.scrollBy(0, by * innerHeight * 0.8);
    return this.view();
  },

  // view is what the page is and which part of it is on the screen.
  view() {
    return { title: document.title, url: location.href, y: Math.round(scrollY), height: document.documentElement.scrollHeight,
      width: innerWidth, screen: innerHeight };
  },
})
