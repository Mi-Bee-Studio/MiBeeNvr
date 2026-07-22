/**
 * WebSocket binary wire protocol for WebCodecs Player.
 *
 * Message types:
 *   0x01 = codec_info    (server → client)
 *   0x02 = video_frame   (server → client)
 *   0x03 = audio_frame   (server → client)
 *   0x04 = keyframe_req  (client → server)
 *   0x05 = audio_codec_info (server → client)
 *   0xff = eos           (server → client)
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
  AudioCodecInfo: 0x05,
  EOS: 0xff,
} as const;

export type MsgType = (typeof MsgType)[keyof typeof MsgType];

/** Codec identifier strings matching Go wsstream package. */
export const CodecId = {
  H264: 'h264',
  H265: 'h265',
  MJPEG: 'mjpeg',
} as const;

export type CodecId = (typeof CodecId)[keyof typeof CodecId];

/**
 * CodecInfo: codec configuration data sent once at stream start.
 * Binary wire format:
 *   {type:1}{codec:1}{profile:1}{level:1}{sps_len:2}{sps}{pps_len:2}{pps}[vps_len:2][vps]
 *   where codec byte is 4=H.264, 5=H.265, 6=MJPEG.
 * MJPEG sends only {type:1}{codec:1} (no SPS/PPS/VPS).
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

/**
 * AudioCodecInfo: audio codec configuration sent once when audio is present.
 * Binary wire format:
 *   {type:1}{codec:1}{sample_rate:4_BE}{channels:1}
 *   codec byte: 0x01=G.711 μ-law, 0x02=G.711 A-law, 0x03=Opus, 0x04=AAC
 */
export interface AudioCodecInfo {
  codec: number; // audio codec byte (0x01-0x04)
  sampleRate: number; // sample rate in Hz (e.g. 8000)
  channels: number; // number of audio channels (1=mono)
}

/**
 * AudioFrame: a single audio frame for playback.
 * Binary wire format:
 *   {type:1}{pts:8_BE}{codec:1}{data_len:4_BE}{data}
 */
export interface AudioFrame {
  pts: number; // presentation timestamp in 90kHz clock
  codec: number; // audio codec byte
  data: Uint8Array; // encoded audio data
}

/** Audio codec byte constants. */
export const AudioCodecId = {
  MuLaw: 0x01,
  ALaw: 0x02,
  Opus: 0x03,
  AAC: 0x04,
} as const;
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

  dv.setUint8(off, MsgType.CodecInfo);
  off += 1;
  dv.setUint8(off, codecByte);
  off += 1;
  dv.setUint8(off, ci.profile);
  off += 1;
  dv.setUint8(off, ci.level);
  off += 1;

  dv.setUint16(off, spsLen);
  off += 2;
  new Uint8Array(buf, off, spsLen).set(ci.sps);
  off += spsLen;

  dv.setUint16(off, ppsLen);
  off += 2;
  new Uint8Array(buf, off, ppsLen).set(ci.pps);
  off += ppsLen;

  if (hasVps && ci.vps) {
    dv.setUint16(off, vpsLen);
    off += 2;
    new Uint8Array(buf, off, vpsLen).set(ci.vps);
    off += vpsLen;
  }

  return buf;
}

// ─── VideoFrame Encode / Decode ──────────────────────────────────────

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
    const naluLen = dv.getUint32(off);
    off += 4;
    if (off + naluLen > data.byteLength) throw new Error(`VideoFrame truncated at NALU ${i} data`);
    nalus.push(new Uint8Array(data, off, naluLen));
    off += naluLen;
  }

  return { pts, isKeyframe, nalus };
}

// ─── AudioCodecInfo Decode ────────────────────────────────────────────────

/** Decode a binary ArrayBuffer to an AudioCodecInfo. */
export function decodeAudioCodecInfo(data: ArrayBuffer): AudioCodecInfo {
  if (data.byteLength < 7) {
    throw new Error(`AudioCodecInfo too short: ${data.byteLength} bytes`);
  }

  const dv = new DataView(data);
  if (dv.getUint8(0) !== MsgType.AudioCodecInfo) {
    throw new Error(`Expected msg type 0x05, got 0x${dv.getUint8(0).toString(16)}`);
  }

  const codec = dv.getUint8(1);
  const sampleRate = dv.getUint32(2);
  const channels = dv.getUint8(6);

  return { codec, sampleRate, channels };
}

// ─── AudioFrame Decode ────────────────────────────────────────────────────

/** Decode a binary ArrayBuffer to an AudioFrame. */
export function decodeAudioFrame(data: ArrayBuffer): AudioFrame {
  if (data.byteLength < 14) {
    throw new Error(`AudioFrame too short: ${data.byteLength} bytes`);
  }

  const dv = new DataView(data);
  if (dv.getUint8(0) !== MsgType.AudioFrame) {
    throw new Error(`Expected msg type 0x03, got 0x${dv.getUint8(0).toString(16)}`);
  }

  const pts = Number(dv.getBigInt64(1));
  const codec = dv.getUint8(9);
  const dataLen = dv.getUint32(10);

  if (14 + dataLen > data.byteLength) {
    throw new Error(`AudioFrame truncated: expected ${dataLen} bytes, got ${data.byteLength - 14}`);
  }

  const audioData = new Uint8Array(data, 14, dataLen);

  return { pts, codec, data: audioData };
}
