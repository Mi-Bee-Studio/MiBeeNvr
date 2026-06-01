/**
 * WebSocket binary wire protocol for WebCodecs Player.
 *
 * Message types:
 *   0x01 = codec_info   (server → client)
 *   0x02 = video_frame  (server → client)
 *   0x03 = audio_frame  (server → client, reserved)
 *   0x04 = keyframe_req (client → server)
 *
 * All multi-byte integers are big-endian (network byte order).
 * NAL units do NOT include start codes — they are raw payloads.
 *
 * Mirror of internal/wsstream/ (Go backend).
 */

/** Message type constants matching Go wsstream package. */
export const MsgType = {
  CodecInfo: 0x01,
  VideoFrame: 0x02,
  AudioFrame: 0x03,
  KeyframeReq: 0x04,
  EOS: 0xFF,
} as const;

export type MsgType = (typeof MsgType)[keyof typeof MsgType];

/** Codec identifier strings matching Go wsstream package. */
export const CodecId = {
  H264: "h264",
  H265: "h265",
} as const;

export type CodecId = (typeof CodecId)[keyof typeof CodecId];

/**
 * CodecInfo: codec configuration data sent once at stream start.
 * Binary wire format:
 *   {type:1}{codec:1}{profile:1}{level:1}{sps_len:2}{sps}{pps_len:2}{pps}[vps_len:2][vps]
 *   where codec byte is 4=H.264, 5=H.265.
 */
export interface CodecInfo {
  codec: CodecId;
  profile: number;
  level: number;
  sps: Uint8Array;
  pps: Uint8Array;
  vps?: Uint8Array; // H.265 only
}

/**
 * VideoFrame: a single video frame with its NAL units.
 * Binary wire format:
 *   {type:2}{pts:8}{is_keyframe:1}{nalu_count:2}{nalu1_len:4}{nalu1}...
 */
export interface VideoFrame {
  pts: number; // 90kHz clock, fits in JS safe integer range
  isKeyframe: boolean;
  nalus: Uint8Array[];
}

// ─── CodecInfo Encode / Decode ───────────────────────────────────────

/** Encode a CodecInfo to a binary ArrayBuffer. */
export function encodeCodecInfo(ci: CodecInfo): ArrayBuffer {
  const codecByte = ci.codec === CodecId.H265 ? 5 : 4;

  const spsLen = ci.sps.byteLength;
  const ppsLen = ci.pps.byteLength;
  const vpsLen = ci.vps?.byteLength ?? 0;
  const hasVps = ci.codec === CodecId.H265;

  // type(1) + codec(1) + profile(1) + level(1) + sps_len(2) + sps + pps_len(2) + pps + [vps_len(2) + vps]
  const size = 1 + 1 + 1 + 1 + 2 + spsLen + 2 + ppsLen + (hasVps ? 2 + vpsLen : 0);

  const buf = new ArrayBuffer(size);
  const dv = new DataView(buf);
  let off = 0;

  dv.setUint8(off, MsgType.CodecInfo); off += 1;
  dv.setUint8(off, codecByte); off += 1;
  dv.setUint8(off, ci.profile); off += 1;
  dv.setUint8(off, ci.level); off += 1;

  dv.setUint16(off, spsLen); off += 2;
  new Uint8Array(buf, off, spsLen).set(ci.sps); off += spsLen;

  dv.setUint16(off, ppsLen); off += 2;
  new Uint8Array(buf, off, ppsLen).set(ci.pps); off += ppsLen;

  if (hasVps && ci.vps) {
    dv.setUint16(off, vpsLen); off += 2;
    new Uint8Array(buf, off, vpsLen).set(ci.vps); off += vpsLen;
  }

  return buf;
}

/** Decode a binary ArrayBuffer to a CodecInfo. */
export function decodeCodecInfo(data: ArrayBuffer): CodecInfo {
  if (data.byteLength < 5) {
    throw new Error(`CodecInfo too short: ${data.byteLength} bytes`);
  }

  const dv = new DataView(data);
  if (dv.getUint8(0) !== MsgType.CodecInfo) {
    throw new Error(`Expected msg type 0x01, got 0x${dv.getUint8(0).toString(16)}`);
  }

  const codecByte = dv.getUint8(1);
  const codec: CodecId = codecByte === 5 ? CodecId.H265 : CodecId.H264;
  const profile = dv.getUint8(2);
  const level = dv.getUint8(3);

  let off = 4;

  const spsLen = dv.getUint16(off); off += 2;
  if (off + spsLen > data.byteLength) throw new Error("CodecInfo truncated at SPS");
  const sps = new Uint8Array(data, off, spsLen); off += spsLen;

  const ppsLen = dv.getUint16(off); off += 2;
  if (off + ppsLen > data.byteLength) throw new Error("CodecInfo truncated at PPS");
  const pps = new Uint8Array(data, off, ppsLen); off += ppsLen;

  let vps: Uint8Array | undefined;
  if (codec === CodecId.H265) {
    const vpsLen = dv.getUint16(off); off += 2;
    if (off + vpsLen > data.byteLength) throw new Error("CodecInfo truncated at VPS");
    vps = new Uint8Array(data, off, vpsLen); off += vpsLen;
  }

  return { codec, profile, level, sps, pps, vps };
}

// ─── VideoFrame Encode / Decode ──────────────────────────────────────

/** Encode a VideoFrame to a binary ArrayBuffer. */
export function encodeVideoFrame(vf: VideoFrame): ArrayBuffer {
  if (vf.nalus.length > 65535) {
    throw new Error(`Too many NALUs: ${vf.nalus.length}`);
  }

  // type(1) + pts(8) + isKeyframe(1) + naluCount(2)
  let size = 1 + 8 + 1 + 2;
  for (const nalu of vf.nalus) {
    size += 4 + nalu.byteLength;
  }

  const buf = new ArrayBuffer(size);
  const dv = new DataView(buf);
  let off = 0;

  dv.setUint8(off, MsgType.VideoFrame); off += 1;
  dv.setBigInt64(off, BigInt(vf.pts)); off += 8;
  dv.setUint8(off, vf.isKeyframe ? 1 : 0); off += 1;
  dv.setUint16(off, vf.nalus.length); off += 2;

  for (const nalu of vf.nalus) {
    dv.setUint32(off, nalu.byteLength); off += 4;
    new Uint8Array(buf, off, nalu.byteLength).set(nalu); off += nalu.byteLength;
  }

  return buf;
}

/** Decode a binary ArrayBuffer to a VideoFrame. */
export function decodeVideoFrame(data: ArrayBuffer): VideoFrame {
  if (data.byteLength < 12) {
    throw new Error(`VideoFrame too short: ${data.byteLength} bytes`);
  }

  const dv = new DataView(data);
  if (dv.getUint8(0) !== MsgType.VideoFrame) {
    throw new Error(`Expected msg type 0x02, got 0x${dv.getUint8(0).toString(16)}`);
  }

  const pts = Number(dv.getBigInt64(1));
  const isKeyframe = dv.getUint8(9) !== 0;
  const naluCount = dv.getUint16(10);

  let off = 12;
  const nalus: Uint8Array[] = [];

  for (let i = 0; i < naluCount; i++) {
    if (off + 4 > data.byteLength) throw new Error(`VideoFrame truncated at NALU ${i} length`);
    const naluLen = dv.getUint32(off); off += 4;
    if (off + naluLen > data.byteLength) throw new Error(`VideoFrame truncated at NALU ${i} data`);
    nalus.push(new Uint8Array(data, off, naluLen));
    off += naluLen;
  }

  return { pts, isKeyframe, nalus };
}
