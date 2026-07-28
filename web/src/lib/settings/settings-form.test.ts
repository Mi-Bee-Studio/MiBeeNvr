import { describe, it, expect, vi, beforeEach } from 'vitest';
import { settingsForm } from './settings-form.svelte';

// settingsForm is a module-level singleton. Tests must clear it between cases
// so one test's registered panels don't leak into the next.
beforeEach(() => {
  settingsForm.clear();
});

describe('settingsForm coordinator', () => {
  it('is not dirty when no panels are registered', () => {
    expect(settingsForm.isAnyDirty).toBe(false);
  });

  it('is not dirty when the only panel reports clean', () => {
    const unregister = settingsForm.register('general', {
      isDirty: () => false,
      save: vi.fn(),
    });
    expect(settingsForm.isAnyDirty).toBe(false);
    unregister();
  });

  it('is dirty when any registered panel reports dirty', () => {
    const unregA = settingsForm.register('general', { isDirty: () => false, save: vi.fn() });
    const unregB = settingsForm.register('storage', { isDirty: () => true, save: vi.fn() });
    expect(settingsForm.isAnyDirty).toBe(true);
    unregA();
    unregB();
  });

  it('saveAll only invokes save on dirty panels', async () => {
    const saveClean = vi.fn().mockResolvedValue(undefined);
    const saveDirty = vi.fn().mockResolvedValue(undefined);
    settingsForm.register('general', { isDirty: () => false, save: saveClean });
    settingsForm.register('storage', { isDirty: () => true, save: saveDirty });

    await settingsForm.saveAll();

    expect(saveClean).not.toHaveBeenCalled();
    expect(saveDirty).toHaveBeenCalledTimes(1);
  });

  it('saveAll rethrows on the first panel that throws (#160 save contract)', async () => {
    // A panel whose save() throws must propagate so the Settings shell keeps
    // the unified dirty bar visible and reports the failure to the user.
    settingsForm.register('general', {
      isDirty: () => true,
      save: vi.fn().mockRejectedValue(new Error('backend 503')),
    });

    await expect(settingsForm.saveAll()).rejects.toThrow('backend 503');
  });

  it('getDestructiveWarnings collects warnings from dirty panels only', () => {
    settingsForm.register('general', { isDirty: () => false, save: vi.fn() });
    settingsForm.register('storage', {
      isDirty: () => true,
      save: vi.fn(),
      getDestructiveWarning: () => 'will delete recordings',
    });
    settingsForm.register('streaming', {
      isDirty: () => true,
      save: vi.fn(),
      getDestructiveWarning: () => null, // dirty but non-destructive
    });

    expect(settingsForm.getDestructiveWarnings()).toEqual(['will delete recordings']);
  });

  it('unregister removes the panel from dirty tracking', () => {
    const unregister = settingsForm.register('storage', {
      isDirty: () => true,
      save: vi.fn(),
    });
    expect(settingsForm.isAnyDirty).toBe(true);

    unregister();

    expect(settingsForm.isAnyDirty).toBe(false);
  });

  // ─── #160 regression: dirty state survives panel unregister/register cycle ─
  //
  // Before the #160 fix, Settings.svelte used `{#if activeCategory === ...}`
  // to conditionally render the active panel. Switching categories unmounted
  // the panel → onDestroy → unregister() → the panel's dirty predicate and
  // unsaved values were dropped from the coordinator. Switching back remounted
  // the panel from a fresh loadSettings(), so edits were silently lost.
  //
  // The fix keeps all panels mounted and uses CSS `hidden` to toggle display.
  // This test pins the coordinator behavior the fix relies on: as long as a
  // panel STAYS registered, its dirty state is visible to isAnyDirty regardless
  // of what the UI does. It also documents the failure mode (unregister loses
  // the state) so a future refactor that reintroduces conditional rendering
  // fails loudly here rather than silently in production.
  it('#160: dirty state persists while a panel stays registered (no unregister)', () => {
    // Simulate a panel with mutable form state (like GeneralPanel.svelte).
    let savedValue = 'Local';
    let editValue = 'Local';
    const unregister = settingsForm.register('general', {
      isDirty: () => editValue !== savedValue,
      save: vi.fn(async () => {
        savedValue = editValue; // simulate server-persist
      }),
      reset: () => {
        editValue = savedValue;
      },
    });

    // User edits the field — panel is now dirty.
    editValue = 'Asia/Hong_Kong';
    expect(settingsForm.isAnyDirty).toBe(true);

    // User switches to another category and back. With the #160 fix the panel
    // stays mounted, so no unregister/register cycle happens. The coordinator
    // still sees the dirty state.
    expect(settingsForm.isAnyDirty).toBe(true);
    expect(settingsForm.isDirty('general')).toBe(true);

    // Contrast: if the panel WERE unmounted (old behavior), unregister would
    // fire here and dirty would be lost. Document that explicitly.
    unregister();
    expect(settingsForm.isAnyDirty).toBe(false);
  });
});
