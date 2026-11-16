/** Pixel geometry independent of the DOM. Heights include each row's spacing. */
export class VirtualRows {
  readonly offsets: number[] = [0];
  readonly ids: number[];
  /** The rows whose height is the estimate: they were never measured. */
  readonly guessed: boolean[] = [];

  constructor(ids: number[], heights: ReadonlyMap<number, number>, estimate = 96) {
    this.ids = ids;
    for (const id of ids) {
      const height = heights.get(id);
      this.guessed.push(height === undefined);
      this.offsets.push(this.total + (height ?? estimate));
    }
  }

  get total(): number { return this.offsets[this.offsets.length - 1]; }

  index(y: number): number {
    let lo = 0;
    let hi = this.ids.length;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      if (this.offsets[mid + 1] <= y) lo = mid + 1;
      else hi = mid;
    }
    return Math.min(lo, Math.max(0, this.ids.length - 1));
  }

  /** How far what the view shows at top lies further down in next than
   * here: what the scroll position has to follow so that the view stays
   * where it is. It is an ID that is followed, not scrollHeight: an older
   * page can come before the row, and measurements can change on either side
   * of it. Rows that grow below it move nothing. */
  moved(top: number, next: VirtualRows): number {
    if (!this.ids.length) return 0;
    // Ignore a subpixel sliver of the previous row at a boundary. Browser
    // scrollTop is rounded while row measurements can have fractional pixels.
    const i = this.index(top + 2);
    const j = next.ids.indexOf(this.ids[i]);
    if (j < 0) return 0;
    // A measured row keeps its top, so that an answer that is read while it
    // streams in stays where it is. A row that was never measured is as high
    // as it turns out to be, and what follows it is what stays.
    const edge = this.guessed[i] ? 1 : 0;
    return next.offsets[j + edge] - this.offsets[i + edge];
  }

  window(top: number, height: number, overscan = 700): { start: number; end: number } {
    if (!this.ids.length) return { start: 0, end: 0 };
    return {
      start: this.index(Math.max(0, top - overscan)),
      end: Math.min(this.ids.length, this.index(top + height + overscan) + 1),
    };
  }
}
