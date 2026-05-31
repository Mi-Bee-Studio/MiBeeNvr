/**
 * AI Detection Module — barrel export
 *
 * Re-exports the ONNX Runtime Web integration for use in components.
 * Submodules will be added in later phases (preprocessing, postprocessing, etc.).
 */

export { AiRuntime, MODEL_CACHE_NAME, DEFAULT_MODEL_URL } from './runtime';
export type { AiRuntimeInitOptions, AiRunOptions, AiRunResult } from './runtime';
