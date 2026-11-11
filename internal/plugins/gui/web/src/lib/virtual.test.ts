import { describe, expect, it } from 'vitest';
import { VirtualRows } from './virtual';

describe('virtual row geometry', () => {
  it('keeps the visible message and pixel offset after prepend and changed heights above it', () => {
    const heights = new Map([[1, 50], [2, 120], [3, 80]]);
    const old = new VirtualRows([1, 2, 3], heights);
    const anchor = old.anchor(73)!;
    expect(anchor).toEqual({ id: 2, offset: 23 });
    heights.set(0, 200);
    heights.set(1, 75);
    const next = new VirtualRows([0, 1, 2, 3], heights);
    expect(next.restore(anchor)).toBe(298);
    // Output below the anchor must not influence its position.
    heights.set(3, 900);
    expect(new VirtualRows([0, 1, 2, 3], heights).restore(anchor)).toBe(298);
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
