// The answers of the agent are markdown, as the models write it. The chat shows
// them formatted, as the terminal UI does (internal/tui/markdown.go): headings,
// emphasis, code, lists, quotes, tables and links, without the marks.
//
// Nothing here makes HTML out of text: every node is built with createElement
// and textContent, so that tags and attributes the model wrote stay text. A
// link is not an anchor either, it shows its address after its label, as in the
// terminal, so that no click leaves the window.

const FENCE = /^( {0,3})(`{3,}|~{3,})\s*([^`]*?)\s*$/;
const HEADING = /^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$/;
const RULE = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
const QUOTE = /^ {0,3}>[ ]?/;
const ITEM = /^( *)([-*+]|\d{1,9}[.)])([ \t]+|$)(.*)$/;
const DELIMITER = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;
const SETEXT = /^ {0,3}(=+|-{2,})[ \t]*$/;
const PUNCTUATION = '!"#$%&\'()*+,-./:;<=>?@[\\]^_`{|}~';

const blank = line => line.trim() === '';
const indentOf = line => line.length - line.trimStart().length;

function element(doc, tag, className, text) {
  const node = doc.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function textNode(doc, text) {
  return doc.createTextNode(text);
}

// The index of the delimiter that closes the one at start, or -1. Code spans
// and escapes in between are skipped.
function closer(text, start, mark) {
  const word = mark[0] === '_';
  for (let i = start + mark.length; i < text.length; i++) {
    const c = text[i];
    if (c === '\\') { i++; continue; }
    if (c === '`') {
      let run = 1;
      while (text[i + run] === '`') run++;
      const end = text.indexOf('`'.repeat(run), i + run);
      if (end >= 0) i = end + run - 1; else i += run - 1;
      continue;
    }
    if (!text.startsWith(mark, i)) continue;
    if (/\s/.test(text[i - 1] ?? ' ')) continue;
    if (mark.length === 1 && (text[i + 1] === c || text[i - 1] === c)) continue;
    if (word && /[\p{L}\p{N}]/u.test(text[i + mark.length] ?? '')) continue;
    return i;
  }
  return -1;
}

// The end of the bracket that opens at start, or -1.
function bracketEnd(text, start, open, close) {
  let depth = 0;
  for (let i = start; i < text.length; i++) {
    if (text[i] === '\\') { i++; continue; }
    if (text[i] === open) depth++;
    else if (text[i] === close && --depth === 0) return i;
  }
  return -1;
}

// The inline marks of text, appended to parent.
function inline(text, parent, doc, bare = true) {
  let plain = '';
  const flush = () => {
    if (plain) parent.append(textNode(doc, plain));
    plain = '';
  };
  const wrap = (tag, inner, className) => {
    flush();
    const node = element(doc, tag, className);
    inline(inner, node, doc, bare);
    parent.append(node);
  };
  // A mark without a closer after some opener has none after a later one
  // either, so that text full of lone stars is not scanned again and again.
  const unclosed = new Map();
  let i = 0;
  while (i < text.length) {
    const c = text[i];
    if (c === '\\' && i + 1 < text.length && PUNCTUATION.includes(text[i + 1])) {
      plain += text[i + 1];
      i += 2;
      continue;
    }
    if (c === '`') {
      let run = 1;
      while (text[i + run] === '`') run++;
      const end = text.indexOf('`'.repeat(run), i + run);
      if (end >= 0 && text[end + run] !== '`') {
        flush();
        let code = text.slice(i + run, end).replace(/\n/g, ' ');
        if (code.length > 2 && code.startsWith(' ') && code.endsWith(' ') && code.trim()) code = code.slice(1, -1);
        parent.append(element(doc, 'code', '', code));
        i = end + run;
        continue;
      }
      plain += text.slice(i, i + run);
      i += run;
      continue;
    }
    if (c === '!' && text[i + 1] === '[' || c === '[') {
      const open = c === '!' ? i + 1 : i;
      const end = bracketEnd(text, open, '[', ']');
      if (end >= 0 && text[end + 1] === '(') {
        const stop = bracketEnd(text, end + 1, '(', ')');
        if (stop >= 0) {
          const label = text.slice(open + 1, end);
          const target = text.slice(end + 2, stop).trim().replace(/^<(.*)>$/, '$1').replace(/\s+"[^"]*"$/, '');
          flush();
          if (c === '!') {
            parent.append(element(doc, 'span', 'md-muted', '[image: ' + label + '] ' + target));
          } else {
            const link = element(doc, 'span', 'md-link');
            inline(label, link, doc, false);
            parent.append(link);
            if (target && target !== label) parent.append(element(doc, 'span', 'md-muted', ' (' + target + ')'));
          }
          i = stop + 1;
          continue;
        }
      }
    }
    if (c === '<') {
      const match = /^<((?:https?:\/\/|mailto:)[^\s<>]+)>/.exec(text.slice(i));
      if (match) {
        flush();
        parent.append(element(doc, 'span', 'md-link', match[1]));
        i += match[0].length;
        continue;
      }
    }
    if (bare && (c === 'h' || c === 'w') && !/[\p{L}\p{N}]/u.test(text[i - 1] ?? ' ')) {
      const match = /^(?:https?:\/\/|www\.)[^\s<]+/.exec(text.slice(i));
      if (match) {
        const url = match[0].replace(/[?!.,:;*_~'")\]]+$/, '');
        flush();
        parent.append(element(doc, 'span', 'md-link', url));
        i += url.length;
        continue;
      }
    }
    if (c === '*' || c === '_' || c === '~') {
      let mark = '';
      if (text.startsWith(c.repeat(2), i)) mark = c.repeat(2);
      else if (c !== '~') mark = c;
      const next = text[i + mark.length] ?? ' ';
      const inWord = c === '_' && /[\p{L}\p{N}]/u.test(text[i - 1] ?? ' ');
      if (mark && !/\s/.test(next) && !inWord && !(unclosed.get(mark) <= i)) {
        const end = closer(text, i, mark);
        if (end < 0) unclosed.set(mark, i);
        if (end > i + mark.length) {
          wrap(c === '~' ? 'del' : mark.length === 2 ? 'strong' : 'em', text.slice(i + mark.length, end));
          i = end + mark.length;
          continue;
        }
      }
      plain += c;
      i++;
      continue;
    }
    plain += c;
    i++;
  }
  flush();
}

function splitRow(line) {
  let row = line.trim();
  if (row.startsWith('|')) row = row.slice(1);
  if (row.endsWith('|') && !row.endsWith('\\|')) row = row.slice(0, -1);
  const cells = [];
  let cell = '';
  for (let i = 0; i < row.length; i++) {
    if (row[i] === '\\' && row[i + 1] === '|') { cell += '|'; i++; continue; }
    if (row[i] === '`') {
      const end = row.indexOf('`', i + 1);
      if (end >= 0) { cell += row.slice(i, end + 1); i = end; continue; }
    }
    if (row[i] === '|') { cells.push(cell.trim()); cell = ''; continue; }
    cell += row[i];
  }
  cells.push(cell.trim());
  return cells;
}

function startsBlock(line, first = false) {
  if (FENCE.test(line) || HEADING.test(line) || RULE.test(line) || QUOTE.test(line)) return true;
  const item = ITEM.exec(line);
  if (!item || item[1].length > 3 || blank(item[4])) return false;
  return /^[-*+]$/.test(item[2]) || /^1[.)]$/.test(item[2]) || first;
}

function tableAt(lines, i) {
  if (i + 1 >= lines.length || !lines[i].includes('|') || !DELIMITER.test(lines[i + 1])) return null;
  const head = splitRow(lines[i]);
  const align = splitRow(lines[i + 1]);
  if (head.length !== align.length) return null;
  return { head, align };
}

// The blocks of lines, appended to parent. In a tight list item a paragraph is
// bare, without a block of its own.
function blocks(lines, parent, doc, tight = false) {
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (blank(line)) { i++; continue; }

    const fence = FENCE.exec(line);
    if (fence) {
      const [, indent, marks, info] = fence;
      const body = [];
      i++;
      while (i < lines.length) {
        const close = /^ {0,3}(`{3,}|~{3,})\s*$/.exec(lines[i]);
        if (close && close[1][0] === marks[0] && close[1].length >= marks.length) { i++; break; }
        body.push(lines[i].replace(new RegExp('^ {0,' + indent.length + '}'), ''));
        i++;
      }
      const pre = element(doc, 'pre', 'md-code');
      const code = element(doc, 'code', '', body.join('\n'));
      const lang = info.split(/\s+/)[0];
      if (/^[\w+#.-]+$/.test(lang)) code.className = 'language-' + lang;
      pre.append(code);
      parent.append(pre);
      continue;
    }

    if (/^( {4}|\t)/.test(line)) {
      const body = [];
      while (i < lines.length && (blank(lines[i]) || /^( {4}|\t)/.test(lines[i]))) {
        body.push(lines[i].replace(/^( {4}|\t)/, ''));
        i++;
      }
      while (body.length && blank(body[body.length - 1])) body.pop();
      const pre = element(doc, 'pre', 'md-code');
      pre.append(element(doc, 'code', '', body.join('\n')));
      parent.append(pre);
      continue;
    }

    const heading = HEADING.exec(line);
    if (heading) {
      const node = element(doc, 'h4', 'md-heading md-h' + heading[1].length);
      inline(heading[2] ?? '', node, doc);
      parent.append(node);
      i++;
      continue;
    }

    if (RULE.test(line)) {
      parent.append(element(doc, 'hr', 'md-rule'));
      i++;
      continue;
    }

    if (QUOTE.test(line)) {
      const body = [];
      while (i < lines.length && !blank(lines[i]) && (QUOTE.test(lines[i]) || !startsBlock(lines[i]))) {
        body.push(lines[i].replace(QUOTE, ''));
        i++;
      }
      const quote = element(doc, 'blockquote', 'md-quote');
      blocks(body, quote, doc);
      parent.append(quote);
      continue;
    }

    const item = ITEM.exec(line);
    if (item && item[1].length <= 3) {
      i = list(lines, i, parent, doc);
      continue;
    }

    const table = tableAt(lines, i);
    if (table) {
      const node = element(doc, 'table', 'md-table');
      const row = (cells, tag, parentRow) => {
        table.head.forEach((_, index) => {
          const cell = element(doc, tag);
          const align = table.align[index];
          if (/^:-*:$/.test(align)) cell.className = 'align-center';
          else if (/-:$/.test(align)) cell.className = 'align-right';
          inline(cells[index] ?? '', cell, doc);
          parentRow.append(cell);
        });
      };
      const head = element(doc, 'thead');
      const headRow = element(doc, 'tr');
      row(table.head, 'th', headRow);
      head.append(headRow);
      node.append(head);
      const body = element(doc, 'tbody');
      i += 2;
      while (i < lines.length && !blank(lines[i]) && lines[i].includes('|') && !startsBlock(lines[i])) {
        const bodyRow = element(doc, 'tr');
        row(splitRow(lines[i]), 'td', bodyRow);
        body.append(bodyRow);
        i++;
      }
      node.append(body);
      parent.append(node);
      continue;
    }

    // A paragraph keeps its line breaks, as the terminal UI does.
    const text = [line.trim()];
    i++;
    let level = 0;
    while (i < lines.length && !blank(lines[i])) {
      const setext = SETEXT.exec(lines[i]);
      if (setext) { level = setext[1][0] === '=' ? 1 : 2; i++; break; }
      if (startsBlock(lines[i]) || tableAt(lines, i)) break;
      text.push(lines[i].trim());
      i++;
    }
    const node = level
      ? element(doc, 'h4', 'md-heading md-h' + level)
      : element(doc, tight ? 'span' : 'p', tight ? 'md-tight' : '');
    inline(text.join('\n'), node, doc);
    parent.append(node);
  }
}

// The list that starts at lines[start], appended to parent; the index after it.
function list(lines, start, parent, doc) {
  const first = ITEM.exec(lines[start]);
  const ordered = /\d/.test(first[2]);
  const bullet = first[2];
  const same = marker => ordered ? /\d/.test(marker) && marker.slice(-1) === first[2].slice(-1) : marker === bullet;
  const node = element(doc, ordered ? 'ol' : 'ul', 'md-list');
  if (ordered && parseInt(first[2], 10) !== 1) node.setAttribute('start', String(parseInt(first[2], 10)));
  const items = [];
  let i = start, gap = false;
  while (i < lines.length) {
    const head = ITEM.exec(lines[i]);
    if (!head || !same(head[2]) || (items.length && indentOf(lines[i]) >= items[items.length - 1].offset)) break;
    const spaces = blank(head[4]) ? 1 : Math.min(head[3].replace(/\t/g, '    ').length, 4);
    const offset = head[1].length + head[2].length + spaces;
    const body = [head[4]];
    i++;
    let held = 0;
    while (i < lines.length) {
      const next = lines[i];
      if (blank(next)) { held++; body.push(''); i++; continue; }
      if (indentOf(next) >= offset) {
        body.push(next.slice(offset));
        held = 0;
        i++;
        continue;
      }
      if (held === 0 && !startsBlock(next, true) && !tableAt(lines, i)) { body.push(next.trim()); i++; continue; }
      break;
    }
    while (body.length && blank(body[body.length - 1])) { body.pop(); held++; }
    items.push({ body, offset });
    const resumes = i < lines.length && ITEM.exec(lines[i]) && same(ITEM.exec(lines[i])[2]) && indentOf(lines[i]) < offset;
    if (held > 0 && resumes) gap = true;
  }
  for (const { body } of items) {
    const li = element(doc, 'li');
    const task = /^\[([ xX])\][ \t]+/.exec(body[0] ?? '');
    if (task) {
      body[0] = body[0].slice(task[0].length);
      li.className = 'md-task';
      li.append(element(doc, 'span', 'md-check', task[1] === ' ' ? '☐ ' : '☑ '));
    }
    blocks(body, li, doc, !gap);
    node.append(li);
  }
  parent.append(node);
  return i;
}

// The source cut to at most limit characters between two blocks, so that no
// element is cut in the middle; a code block that is cut is closed.
export function markdownPreview(source, limit) {
  if (source.length <= limit) return source;
  const lines = source.split('\n');
  let used = 0, fence = null, cut = -1, before = 0, next = '';
  const shown = [];
  for (const line of lines) {
    if (used + line.length + 1 > limit) { next = line; break; }
    shown.push(line);
    used += line.length + 1;
    const open = FENCE.exec(line);
    if (open && !fence) fence = open[2];
    else if (open && fence && open[2][0] === fence[0] && open[2].length >= fence.length && !open[3]) fence = null;
    if (blank(line) && !fence) { cut = shown.length; before = used; }
  }
  if (cut > 0 && before >= limit / 2) return shown.slice(0, cut).join('\n').trimEnd();
  // Whole blocks or lines would show too little of it, as a long paragraph or
  // a line of minified JSON does: the line after them is cut instead.
  if (used < limit / 2) shown.push(next.slice(0, limit - used));
  const text = shown.join('\n');
  return fence ? text + '\n' + fence : text;
}

// The element that shows source as formatted text.
export function renderMarkdown(source, doc = document) {
  const root = element(doc, 'div', 'markdown');
  blocks(source.replace(/\r\n?/g, '\n').split('\n'), root, doc);
  return root;
}
