import { describe, expect, it } from 'vitest';
import { VirtualRows } from './virtual';

describe('virtual row geometry', () => {
  it('keeps the visible message and pixel offset after prepend and changed heights above it', () => {
    const heights = new Map([[1, 50], [2, 120], [3, 80]]);
    const old = new VirtualRows([1, 2, 3], heights);
    // 73 is 23 pixels into the second row.
    heights.set(0, 200);
    heights.set(1, 75);
    const next = new VirtualRows([0, 1, 2, 3], heights);
    expect(old.moved(73, next)).toBe(298 - 73);
    // Output below the row must not influence its position, and neither does
    // the row itself when it grows: an answer that is read as it streams in.
    heights.set(3, 900);
    heights.set(2, 500);
    expect(old.moved(73, new VirtualRows([0, 1, 2, 3, 4], heights))).toBe(298 - 73);
    heights.set(1, 20);
    expect(old.moved(73, new VirtualRows([1, 2, 3], heights))).toBe(-30);
    // A sliver of the row before it at the boundary is not what is shown.
    expect(old.moved(49, new VirtualRows([1, 2, 3], heights))).toBe(-30);
  });

  it('moves nothing for what streams in below the view, or when there is nothing to follow', () => {
    const heights = new Map([[1, 50], [2, 120], [3, 80]]);
    const old = new VirtualRows([1, 2, 3], heights);
    heights.set(3, 900);
    expect(old.moved(73, new VirtualRows([1, 2, 3, 4], heights))).toBe(0);
    expect(old.moved(73, new VirtualRows([7, 8], heights))).toBe(0);
    expect(new VirtualRows([], heights).moved(0, old)).toBe(0);
  });

  it('keeps what follows a row that was never measured, as high as it turns out to be', () => {
    const heights = new Map([[2, 120]]);
    const old = new VirtualRows([1, 2], heights, 96);
    expect(old.guessed).toEqual([true, false]);
    // The view shows the end of the first row, and the second one below it.
    heights.set(1, 63);
    expect(old.moved(70, new VirtualRows([1, 2], heights, 96))).toBe(-33);
    heights.set(0, 40);
    expect(old.moved(70, new VirtualRows([0, 1, 2], heights, 96))).toBe(7);
    // The last row is one too: the end of what is loaded is what stays.
    const last = new VirtualRows([2, 3], heights, 96);
    heights.set(3, 30);
    expect(last.moved(130, new VirtualRows([2, 3], heights, 96))).toBe(-66);
  });

  it('windows both directions with spacers accounting for all loaded rows', () => {
    const rows = new VirtualRows(Array.from({ length: 1000 }, (_, i) => i), new Map(), 100);
    expect(rows.total).toBe(100000);
    const middle = rows.window(50000, 600, 700);
    expect(middle).toEqual({ start: 493, end: 514 });
    expect(rows.window(0, 600).start).toBe(0);
    expect(rows.window(99400, 600).end).toBe(1000);
    expect(rows.offsets[middle.start] + (rows.offsets[middle.end] - rows.offsets[middle.start]) + (rows.total - rows.offsets[middle.end])).toBe(rows.total);
    expect(new VirtualRows([], new Map()).window(0, 600)).toEqual({ start: 0, end: 0 });
  });
});
