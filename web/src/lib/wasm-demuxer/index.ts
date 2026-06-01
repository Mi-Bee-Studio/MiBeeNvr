/**
 * WASM Demuxer — barrel exports.
 *
 * Currently implements NALU parsing for WebSocket binary transport.
 * Future: FLV/MPEG-TS demuxing for HTTP-FLV fallback.
 */
export {
  parseAccessUnit,
  isKeyframeNalus,
  type NaluInfo,
  type H264NaluType,
  type H265NaluType,
  type Codec,
} from './nalu-parser';
