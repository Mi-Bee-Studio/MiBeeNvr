/**
 * Unified settings form state coordinator (#153).
 *
 * Each settings panel registers itself with a dirty-check predicate, a save
 * function, and an optional destructive-warning detector. The Settings shell
 * uses this to:
 *   1. Show a single unified sticky Save button
 *   2. Track dirty state across ALL panels for the navigation guard
 *   3. Detect destructive changes before saving (confirmation dialogs)
 */

export interface PanelFormHandle {
  /** Returns true if the panel has unsaved changes. */
  isDirty: () => boolean;
  /** Saves the panel's changes. Throws on error. */
  save: () => Promise<void>;
  /** Resets the panel's form to the last-saved state (Reset button). */
  reset?: () => void;
  /**
   * Returns a warning message if the current pending changes are destructive,
   * or null if non-destructive. The shell collects all non-null warnings and
   * shows a single confirmation dialog before saving.
   */
  getDestructiveWarning?: () => string | null;
}

class SettingsFormCoordinator {
  private panels: Map<string, PanelFormHandle> = $state(new Map());

  register(panelId: string, handle: PanelFormHandle): () => void {
    this.panels.set(panelId, handle);
    this.panels = new Map(this.panels); // trigger reactivity
    return () => {
      this.panels.delete(panelId);
      this.panels = new Map(this.panels); // trigger reactivity
    };
  }

  /** True if ANY registered panel has unsaved changes. Reactive. */
  get isAnyDirty(): boolean {
    for (const handle of this.panels.values()) {
      if (handle.isDirty()) return true;
    }
    return false;
  }

  /** True if a specific panel has unsaved changes. */
  isDirty(panelId: string): boolean {
    return this.panels.get(panelId)?.isDirty() ?? false;
  }

  /**
   * Collect destructive-warning messages from all dirty panels.
   * Returns an array of warning strings (empty if no destructive changes).
   */
  getDestructiveWarnings(): string[] {
    const warnings: string[] = [];
    for (const handle of this.panels.values()) {
      if (handle.isDirty() && handle.getDestructiveWarning) {
        const w = handle.getDestructiveWarning();
        if (w) warnings.push(w);
      }
    }
    return warnings;
  }

  /** Save all dirty panels sequentially. Throws on first error. */
  async saveAll(): Promise<void> {
    for (const [, handle] of this.panels) {
      if (handle.isDirty()) {
        await handle.save();
      }
    }
  }

  /** Reset all panels to last-saved state. */
  resetAll(): void {
    for (const handle of this.panels.values()) {
      handle.reset?.();
    }
  }
}

// Singleton — one coordinator per Settings page instance.
export const settingsForm = new SettingsFormCoordinator();
