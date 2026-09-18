// jsdom does not implement URL.createObjectURL/revokeObjectURL (blob URLs are
// a browser-only concept), and vitest 5's own compat wrapper around
// URL.createObjectURL ("makeCompatBlob") crashes on jsdom Blobs with
// "Cannot read properties of undefined (reading '_buffer')". Components under
// test (AviPlayback, snapshot, recordings API, webcodecs-player) call these to
// turn WS/payload bytes into <img>/<video> sources. Replace both functions
// unconditionally — setupFiles run after environment population, so this
// overrides any wrapper vitest installed — with deterministic fake URLs.
let blobUrlSeq = 0;

URL.createObjectURL = () => `blob:http://localhost/vitest-blob-${++blobUrlSeq}`;
URL.revokeObjectURL = () => {};
