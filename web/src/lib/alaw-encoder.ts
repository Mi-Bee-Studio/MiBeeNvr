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
