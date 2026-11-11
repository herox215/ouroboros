import { describe, expect, it } from 'vitest';
import { markdownPreview, renderMarkdown } from './markdown';

// The structure of rendered markdown as a string: tag(children), text as
// itself.
function shape(node: Node): string {
  if (node.nodeType === Node.TEXT_NODE) return node.textContent ?? '';
  const element = node as HTMLElement;
  const tag = element.tagName.toLowerCase();
  const inner = Array.from(element.childNodes, shape).join('');
  return inner ? tag + '(' + inner + ')' : tag;
}

const md = (source: string): string => Array.from(renderMarkdown(source).children, shape).join('');

describe('renderMarkdown', () => {
  it('renders emphasis, code, links and strikethrough without their marks', () => {
    expect(md('**bold** and __bold__')).toBe('p(strong(bold) and strong(bold))');
    expect(md('*it* and _it_ and snake_case_name')).toBe('p(em(it) and em(it) and snake_case_name)');
    expect(md('~~gone~~ **a *b* c**')).toBe('p(del(gone) strong(a em(b) c))');
    expect(md('use `a * b` here')).toBe('p(use code(a * b) here)');
    expect(md('2 * 3 * 4 and a_b')).toBe('p(2 * 3 * 4 and a_b)');
    expect(md('\\*literal\\*')).toBe('p(*literal*)');
    expect(md('[docs](https://x.org/a)')).toBe('p(span(docs)span( (https://x.org/a)))');
    expect(md('[https://x.org](https://x.org)')).toBe('p(span(https://x.org))');
    expect(md('see https://x.org/a, ok')).toBe('p(see span(https://x.org/a), ok)');
    expect(md('![cat](c.png)')).toBe('p(span([image: cat] c.png))');
  });

  it('renders headings, quotes, rules and fenced and indented code', () => {
    expect(md('# One\n\n### Three ###\n\nSetext\n=====')).toBe('h4(One)h4(Three)h4(Setext)');
    expect(md('> quoted\n> more\n\nafter')).toBe('blockquote(p(quoted\nmore))p(after)');
    expect(md('a\n\n---\n\nb')).toBe('p(a)hrp(b)');
    const fenced = renderMarkdown('```go\nfunc main() {}\n  **kept**\n```\ntext');
    const pre = fenced.children[0] as HTMLElement;
    const code = pre.children[0] as HTMLElement;
    expect(pre.tagName).toBe('PRE');
    expect(code.textContent).toBe('func main() {}\n  **kept**');
    expect(code.className).toBe('language-go');
    expect(shape(fenced.children[1])).toBe('p(text)');
    expect(md('```\nunclosed\n**x**')).toBe('pre(code(unclosed\n**x**))');
    expect(md('    indented\n      more\n\nafter')).toBe('pre(code(indented\n  more))p(after)');
  });

  it('renders lists, nested and tight, ordered and task lists', () => {
    expect(md('- one\n- two\n- **three**')).toBe('ul(li(span(one))li(span(two))li(span(strong(three))))');
    expect(md('1. a\n2. b')).toBe('ol(li(span(a))li(span(b)))');
    expect(md('- a\n  - b\n- c')).toBe('ul(li(span(a)ul(li(span(b))))li(span(c)))');
    expect(md('- a\n\n- b')).toBe('ul(li(p(a))li(p(b)))');
    expect(md('- [x] done\n- [ ] open')).toBe('ul(li(span(☑ )span(done))li(span(☐ )span(open)))');
    const start = renderMarkdown('3. c\n4. d').children[0] as HTMLElement;
    expect(start.getAttribute('start')).toBe('3');
    expect(md('text\n- item')).toBe('p(text)ul(li(span(item)))');
    expect(md('- a\n  lazy')).toBe('ul(li(span(a\nlazy)))');
  });

  it('renders tables with their alignment', () => {
    const table = renderMarkdown('| Name | Wert |\n|:--|--:|\n| **a** | 1 |\n| b \\| c | 2 |').children[0] as HTMLElement;
    expect(shape(table)).toBe('table(thead(tr(th(Name)th(Wert)))tbody(tr(td(strong(a))td(1))tr(td(b | c)td(2))))');
    const row = table.children[1].children[0] as HTMLElement;
    expect(row.children[0].className).toBe('');
    expect(row.children[1].className).toBe('align-right');
    expect(md('a | b\nplain')).toBe('p(a | b\nplain)');
  });

  it('keeps the line breaks of a paragraph', () => {
    expect(md('first\nsecond\n\nthird')).toBe('p(first\nsecond)p(third)');
  });

  it('renders many marks that never close in linear time', () => {
    const globs = '*.go _x ~~y '.repeat(20000);
    const started = Date.now();
    const rendered = md(globs);
    expect(Date.now() - started).toBeLessThan(2000);
    expect(rendered).toBe('p(' + globs.trim() + ')');
  });

  it('never makes HTML of what the model wrote', () => {
    const evil = '<img src=x onerror="bad()"> <script>alert(1)</script> <a href="javascript:bad()">x</a>';
    expect(md(evil)).toBe('p(' + evil + ')');
    expect(md('[x](javascript:bad())')).toBe('p(span(x)span( (javascript:bad())))');
    expect(md('**<b onclick=bad()>**')).toBe('p(strong(<b onclick=bad()>))');
    expect(md('<div>\n**x**\n</div>')).toBe('p(<div>\nstrong(x)\n</div>)');
    const walk = (node: Element): string[] => [node.tagName, ...Array.from(node.children).flatMap(walk)];
    const tags = Array.from(
      renderMarkdown(evil + '\n\n```\n<img src=x>\n```\n\n| <b> |\n|-|\n| <i> |').children,
    ).flatMap(walk);
    for (const tag of tags) expect(['IMG', 'SCRIPT', 'A', 'B', 'I', 'DIV']).not.toContain(tag);
  });
});

describe('markdownPreview', () => {
  it('cuts between two blocks and keeps code fences closed', () => {
    const paragraphs = Array.from({ length: 400 }, (_, i) => 'paragraph **' + i + '** with some more words');
    const source = paragraphs.join('\n\n');
    const cut = markdownPreview(source, 8000);
    expect(cut.length).toBeLessThanOrEqual(8000);
    expect(cut.length).toBeGreaterThan(7000);
    expect(paragraphs).toContain(cut.split('\n\n').at(-1));

    const code = '```\n' + 'x'.repeat(30) + '\n'.repeat(1) + 'line\n'.repeat(3000) + '```';
    const closed = markdownPreview('intro\n\n' + code, 8000);
    expect(closed.startsWith('intro\n\n```\n')).toBe(true);
    expect(closed.endsWith('\n```')).toBe(true);
    expect(closed.length).toBeGreaterThan(7000);
    expect(markdownPreview(code, 8000).endsWith('\n```')).toBe(true);
    expect(markdownPreview('short', 8000)).toBe('short');

    const line = 'b'.repeat(9000);
    expect(markdownPreview(line, 8000)).toBe('b'.repeat(8000));
    expect(markdownPreview('\n' + line, 8000)).toBe('\n' + 'b'.repeat(7999));
    expect(markdownPreview('intro\n\n' + line, 8000)).toBe('intro\n\n' + 'b'.repeat(7993));
  });
});
