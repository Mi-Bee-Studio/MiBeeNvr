import { describe, it, expect, beforeEach } from 'vitest';
import { friendlyError } from '$lib/errors';
import { ApiRequestError } from '$lib/api/client';
import { state } from '$lib/i18n';

beforeEach(() => {
  // Force English so the asserted translations are deterministic regardless of
  // the test runner's navigator.language.
  state.currentLang = 'en';
});

describe('friendlyError', () => {
  it('maps an ApiRequestError code to the errors.<code> translation', () => {
    const e = new ApiRequestError('camera not found: cam-1', 'CAMERA_NOT_FOUND');
    expect(friendlyError(e, 'cameras.failedStart')).toBe('Camera not found. It may have been removed.');
  });

  it('falls back to the error message when the code has no translation', () => {
    const e = new ApiRequestError('some novel backend message', 'UNKNOWN_CODE_XYZ');
    expect(friendlyError(e, 'cameras.failedStart')).toBe('some novel backend message');
  });

  it('falls back to the message when there is no code', () => {
    const e = new ApiRequestError('Request timed out');
    expect(friendlyError(e, 'cameras.failedStart')).toBe('Request timed out');
  });

  it('falls back to the provided i18n key for a plain Error with empty message', () => {
    const e = new Error('');
    // 'cameras.failedStart' resolves via t() to a translated string.
    expect(friendlyError(e, 'cameras.failedStart')).not.toBe('cameras.failedStart');
  });

  it('treats a literal fallback (with spaces) as a literal, not a key', () => {
    const e = new Error('');
    expect(friendlyError(e, 'Something broke here')).toBe('Something broke here');
  });

  it('returns the fallback for a non-Error value', () => {
    expect(friendlyError('weird string value', 'cameras.failedStart')).not.toBe('cameras.failedStart');
    expect(friendlyError(null, 'plain literal')).toBe('plain literal');
  });
});
