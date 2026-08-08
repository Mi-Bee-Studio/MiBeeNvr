import { describe, it, expect } from 'vitest';
import {
  MsgType,
  AudioCodecId,
  decodeAudioCodecInfo,
  decodeAudioFrame,
  type AudioCodecInfo,
} from './protocol';

/**
 * Tests for the audio wire-format decode helpers, focused on the backwards-
 * compatible `config` extension (the trailing {config_len}{config} block added
 * to carry the AAC AudioSpecificConfig). The legacy 7-byte form must still
 * parse, and the new 9+ byte form must surface the config bytes.
 *
 * The Go-side encoder (internal/wsstream/protocol.go EncodeAudioCodecInfo) is
 * the source of truth for the byte layout; these tests mirror it.
 */

/** Build a wire-format AudioCodecInfo packet (mirrors the Go encoder). */
function buildAudioCodecInfoPacket(codec: number, sampleRate: number, channels: number, config?: Uint8Array): ArrayBuffer {
  const configLen = config ? config.length : 0;
  const buf = new ArrayBuffer(9 + configLen);
  const dv = new DataView(buf);
  dv.setUint8(0, MsgType.AudioCodecInfo);
  dv.setUint8(1, codec);
  dv.setUint32(2, sampleRate);
  dv.setUint8(6, channels);
  dv.setUint16(7, configLen);
  if (config && configLen > 0) {
    new Uint8Array(buf, 9, configLen).set(config);
  }
  return buf;
}

describe('decodeAudioCodecInfo', () => {
  it('parses an AAC packet with an AudioSpecificConfig', () => {
    const aasc = new Uint8Array([0x12, 0x10]); // AAC-LC 44100 stereo
    const pkt = buildAudioCodecInfoPacket(AudioCodecId.AAC, 44100, 2, aasc);
    const info = decodeAudioCodecInfo(pkt);
    expect(info.codec).toBe(AudioCodecId.AAC);
    expect(info.sampleRate).toBe(44100);
    expect(info.channels).toBe(2);
    expect(info.config).toBeDefined();
    expect(Array.from(info.config!)).toEqual([0x12, 0x10]);
  });

  it('parses a G.711 packet (config_len = 0 → config undefined)', () => {
    const pkt = buildAudioCodecInfoPacket(AudioCodecId.MuLaw, 8000, 1);
    const info = decodeAudioCodecInfo(pkt);
    expect(info.codec).toBe(AudioCodecId.MuLaw);
    expect(info.sampleRate).toBe(8000);
    expect(info.channels).toBe(1);
    expect(info.config).toBeUndefined();
  });

  it('is backwards-compatible with the legacy 7-byte form (no config block)', () => {
    // A legacy encoder wrote exactly 7 bytes. decodeAudioCodecInfo must still
    // return the codec/rate/channels and leave config undefined.
    const buf = new ArrayBuffer(7);
    const dv = new DataView(buf);
    dv.setUint8(0, MsgType.AudioCodecInfo);
    dv.setUint8(1, AudioCodecId.ALaw);
    dv.setUint32(2, 8000);
    dv.setUint8(6, 1);
    const info = decodeAudioCodecInfo(buf);
    expect(info.codec).toBe(AudioCodecId.ALaw);
    expect(info.sampleRate).toBe(8000);
    expect(info.channels).toBe(1);
    expect(info.config).toBeUndefined();
  });

  it('treats config_len=0 in the new format as no config', () => {
    // 9-byte packet with config_len = 0 (G.711 using the new encoder).
    const pkt = buildAudioCodecInfoPacket(AudioCodecId.MuLaw, 8000, 1, new Uint8Array(0));
    const info = decodeAudioCodecInfo(pkt);
    expect(info.config).toBeUndefined();
  });

  it('rejects a packet that is too short', () => {
    const buf = new ArrayBuffer(6);
    const dv = new DataView(buf);
    dv.setUint8(0, MsgType.AudioCodecInfo);
    expect(() => decodeAudioCodecInfo(buf)).toThrow(/too short/i);
  });

  it('rejects a packet with the wrong message type', () => {
    const buf = new ArrayBuffer(9);
    const dv = new DataView(buf);
    dv.setUint8(0, MsgType.AudioFrame); // wrong type
    dv.setUint8(1, AudioCodecId.AAC);
    expect(() => decodeAudioCodecInfo(buf)).toThrow(/Expected msg type 0x05/);
  });

  it('ignores trailing config bytes beyond the declared length (defensive)', () => {
    // Declare config_len=1 but the buffer has extra trailing bytes; only the
    // declared byte should be returned.
    const aasc = new Uint8Array([0x12, 0xff]); // 2 bytes
    const pkt = buildAudioCodecInfoPacket(AudioCodecId.AAC, 44100, 2, aasc);
    // Append 5 garbage bytes.
    const bigger = new Uint8Array(pkt.byteLength + 5);
    bigger.set(new Uint8Array(pkt), 0);
    bigger.fill(0xee, pkt.byteLength);
    const info = decodeAudioCodecInfo(bigger.buffer);
    expect(info.config).toBeDefined();
    expect(info.config!.length).toBe(2);
    expect(Array.from(info.config!)).toEqual([0x12, 0xff]);
  });
});

describe('decodeAudioFrame', () => {
  it('parses an AAC audio frame with payload', () => {
    // {type:0x03}{pts:8_BE}{codec:0x04}{data_len:4_BE}{data}
    const payload = new Uint8Array([0xde, 0xad, 0xbe, 0xef]);
    const buf = new ArrayBuffer(14 + payload.length);
    const dv = new DataView(buf);
    dv.setUint8(0, MsgType.AudioFrame);
    dv.setBigInt64(1, 123456n);
    dv.setUint8(9, AudioCodecId.AAC);
    dv.setUint32(10, payload.length);
    new Uint8Array(buf, 14, payload.length).set(payload);
    const frame = decodeAudioFrame(buf);
    expect(frame.pts).toBe(123456);
    expect(frame.codec).toBe(AudioCodecId.AAC);
    expect(Array.from(frame.data)).toEqual([0xde, 0xad, 0xbe, 0xef]);
  });

  it('rejects a truncated frame', () => {
    const buf = new ArrayBuffer(13); // < 14 minimum
    const dv = new DataView(buf);
    dv.setUint8(0, MsgType.AudioFrame);
    expect(() => decodeAudioFrame(buf)).toThrow(/too short/i);
  });
});

// Type-only reference to keep the interface in scope for future expansion.
export type { AudioCodecInfo };
