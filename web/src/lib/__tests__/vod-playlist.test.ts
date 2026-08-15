import { describe, expect, it } from 'vitest';
import { entryAt, mediaTimeFor, nearestEntryByWallClock, parseVodPlaylist } from '$lib/vod-playlist';

const PL = (recs: Array<{ rid: string; wall?: string; durs: number[] }>) =>
  '#EXTM3U\n' +
  recs
    .map(
      (r) =>
        (r.wall ? `#EXT-X-PROGRAM-DATE-TIME:${r.wall}\n` : '') +
        `#EXT-X-MAP:URI="/api/cameras/cam1/playback/${r.rid}/init.mp4"\n` +
        r.durs.map((d) => `#EXTINF:${d.toFixed(3)},\n/api/cameras/cam1/playback/${r.rid}/f0-1.m4s`).join('\n'),
    )
    .join('\n#EXT-X-DISCONTINUITY\n') +
  '\n#EXT-X-ENDLIST\n';

describe('parseVodPlaylist', () => {
  it('builds cumulative media starts per EXT-X-MAP period', () => {
    const text = PL([
      { rid: 'a', durs: [4, 4, 4] },
      { rid: 'b', durs: [6] },
    ]);
    const map = parseVodPlaylist(text);
    expect(map).toHaveLength(2);
    expect(map[0]).toMatchObject({ rid: 'a', mediaStart: 0, dur: 12 });
    expect(map[1]).toMatchObject({ rid: 'b', mediaStart: 12, dur: 6 });
  });

  it('attaches the wall-clock anchor of the period that FOLLOWS it', () => {
    const text = PL([
      { rid: 'a', wall: '2026-08-14T10:00:00Z', durs: [8] },
      { rid: 'b', wall: '2026-08-14T11:00:00Z', durs: [8] },
    ]);
    const map = parseVodPlaylist(text);
    expect(map[0].wallStart).toBe(Date.parse('2026-08-14T10:00:00Z'));
    expect(map[1].wallStart).toBe(Date.parse('2026-08-14T11:00:00Z'));
  });

  it('tolerates a playlist without PDT lines', () => {
    const map = parseVodPlaylist(PL([{ rid: 'x', durs: [3] }]));
    expect(map[0].wallStart).toBe(0);
  });
});

describe('entryAt', () => {
  const map = parseVodPlaylist(PL([{ rid: 'a', durs: [10] }, { rid: 'b', durs: [10] }]));
  it('finds the containing period', () => {
    expect(entryAt(map, 0)?.rid).toBe('a');
    expect(entryAt(map, 9.9)?.rid).toBe('a');
    expect(entryAt(map, 10)?.rid).toBe('b');
  });
  it('clamps past the end to the last period', () => {
    expect(entryAt(map, 999)?.rid).toBe('b');
  });
});

describe('nearestEntryByWallClock', () => {
  const map = parseVodPlaylist(
    PL([
      { rid: 'a', wall: '2026-08-14T08:00:00Z', durs: [8] },
      { rid: 'b', wall: '2026-08-14T09:00:00Z', durs: [8] },
      { rid: 'c', wall: '2026-08-14T10:00:00Z', durs: [8] },
    ]),
  );
  it('picks the closest period start', () => {
    expect(nearestEntryByWallClock(map, Date.parse('2026-08-14T09:01:00Z'))?.rid).toBe('b');
    expect(nearestEntryByWallClock(map, Date.parse('2026-08-14T09:40:00Z'))?.rid).toBe('c');
  });
  it('falls back to the first entry when no anchors exist', () => {
    const noWall = parseVodPlaylist(PL([{ rid: 'x', durs: [1] }]));
    expect(nearestEntryByWallClock(noWall, 123)?.rid).toBe('x');
  });
});

describe('mediaTimeFor', () => {
  const map = parseVodPlaylist(PL([{ rid: 'a', durs: [10] }, { rid: 'b', durs: [10] }]));
  it('maps (rid, offset) into the media timeline', () => {
    expect(mediaTimeFor(map, 'b', 5)).toBe(15);
  });
  it('clamps offsets beyond the recording duration', () => {
    expect(mediaTimeFor(map, 'a', 99)).toBe(10);
  });
  it('returns null for unknown rid', () => {
    expect(mediaTimeFor(map, 'zzz', 1)).toBeNull();
  });
});
