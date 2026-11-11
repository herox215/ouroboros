/** Pixel geometry independent of the DOM. Heights include each row's spacing. */
export interface Anchor { id: number; offset: number }

export class VirtualRows {
  readonly offsets: number[] = [0];
  readonly ids: number[];

  constructor(ids: number[], heights: ReadonlyMap<number, number>, estimate = 96) {
    this.ids = ids;
    for (const id of ids) this.offsets.push(this.total + (heights.get(id) ?? estimate));
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

  anchor(top: number): Anchor | null {
    if (!this.ids.length) return null;
    // Ignore a subpixel sliver of the previous row at a boundary. Browser
    // scrollTop is rounded while row measurements can have fractional pixels.
    const i = this.index(top + 2);
    return { id: this.ids[i], offset: top - this.offsets[i] };
  }

  restore(anchor: Anchor): number | null {
    const i = this.ids.indexOf(anchor.id);
    return i < 0 ? null : this.offsets[i] + anchor.offset;
  }

  window(top: number, height: number, overscan = 700): { start: number; end: number } {
    if (!this.ids.length) return { start: 0, end: 0 };
    return {
      start: this.index(Math.max(0, top - overscan)),
      end: Math.min(this.ids.length, this.index(top + height + overscan) + 1),
    };
  }
}
