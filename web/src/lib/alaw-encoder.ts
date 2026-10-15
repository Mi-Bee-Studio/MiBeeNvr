/**
 * PCM → G.711 A-law encoder for GB28181 voice intercom (语音对讲).
 *
 * Mirrors the backend decoders (Go `internal/recorder`, Rust mibee-rec):
 * standard ITU-T G.711 algorithm, no lookup table needed — the segment /
 * mantissa encoding is computed directly. Output is one A-law byte per
 * 16-bit PCM sample (8 kHz mono on the wire).
 */

const CLIP = 32635;

/** Encode one 16-bit PCM sample to an A-law byte. */
export function pcmToAlaw(sample: number): number {
  const sign = sample < 0 ? 0x80 : 0x00;
  let mag = Math.abs(sample);
  if (mag > CLIP) mag = CLIP;

  let seg: number;
  if (mag <= 0x0f) {
    return sign | mag;
  } else if (mag >= 0x800) {
    seg = 7;
  } else if (mag >= 0x400) {
    seg = 6;
  } else if (mag >= 0x200) {
    seg = 5;
  } else if (mag >= 0x100) {
    seg = 4;
  } else if (mag >= 0x80) {
    seg = 3;
  } else if (mag >= 0x40) {
    seg = 2;
  } else if (mag >= 0x20) {
    seg = 1;
  } else {
    seg = 0;
  }
  const mant = (mag >> (seg + 3)) & 0x0f;
  return sign | (seg << 4) | mant;
}

/**
 * Encode a run of PCM samples (Float32, −1..+1, any native rate — resample
 * to 8 kHz BEFORE calling this) into A-law bytes.
 */
export function encodeAlaw(samples: Float32Array): Uint8Array {
  const out = new Uint8Array(samples.length);
  for (let i = 0; i < samples.length; i++) {
    let s = samples[i];
    if (s > 1) s = 1;
    else if (s < -1) s = -1;
    out[i] = pcmToAlaw(Math.round(s * 32767));
  }
  return out;
}

/**
 * Decode one standard G.711 A-law byte to a 16-bit PCM sample
 * (alaw2linear from Sun's public-domain G.711 reference: xor 0x55, then
 * segment expansion). This is the STANDARD wire format used by SIP
 * intercom hardware — unlike pcmToAlaw above, which omits the xor to
 * mirror the GB28181 custom-firmware pair.
 */
export function alawToPcm(a: number): number {
  const v = a ^ 0x55;
  let t = (v & 0x0f) << 4;
  const seg = (v & 0x70) >> 4;
  if (seg === 0) {
    t += 8;
  } else {
    t += 0x108;
    if (seg > 1) t <<= seg - 1;
  }
  return v & 0x80 ? t : -t;
}

/**
 * Decode a run of standard A-law bytes (SIP doorbell RTP payloads) to
 * Float32 PCM (−1..+1, 8 kHz mono).
 */
export function decodeAlaw(bytes: Uint8Array): Float32Array {
  const out = new Float32Array(bytes.length);
  for (let i = 0; i < bytes.length; i++) {
    out[i] = alawToPcm(bytes[i]) / 32768;
  }
  return out;
}

/**
 * Encode PCM to STANDARD A-law bytes (with the xor), for the SIP doorbell
 * talk path — commercial intercom hardware expects the standard form.
 */
export function encodeAlawStandard(samples: Float32Array): Uint8Array {
  const raw = encodeAlaw(samples);
  for (let i = 0; i < raw.length; i++) raw[i] ^= 0x55;
  return raw;
}
