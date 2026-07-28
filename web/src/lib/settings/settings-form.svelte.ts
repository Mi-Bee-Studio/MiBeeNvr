/**
 * Unified settings form state coordinator (#153).
 *
 * This module provides a lightweight coordination layer for the Settings page.
 * Each tab component (GeneralTab, FeaturesTab, AdvancedTab) registers itself
 * with a dirty-check predicate and a save function. The Settings shell uses
 * this to:
 *   1. Show a single unified sticky Save button (instead of 4 separate ones)
 *   2. Track dirty state across ALL tabs for the navigation guard
 *   3. Detect destructive changes before saving (confirmation dialogs)
 *
 * Design: each tab remains autonomous in its form state and API calls — this
 * module just aggregates the dirty + save signals. This avoids a massive
 * centralized state migration while achieving the unified-save UX goal.
 */

export interface TabFormHandle {
  /** Returns true if the tab has unsaved changes. */
  isDirty: () => boolean;
  /** Saves the tab's changes. Throws on error. */
  save: () => Promise<void>;
  /** Resets the tab's form to the last-saved state (Reset button). */
  reset?: () => void;
}

export interface DestructiveChange {
  /** i18n message describing what the destructive change does. */
  message: string;
}

class SettingsFormCoordinator {
  private tabs: Map<string, TabFormHandle> = new Map();

  register(tabId: string, handle: TabFormHandle): () => void {
    this.tabs.set(tabId, handle);
    return () => this.tabs.delete(tabId);
  }

  /** True if ANY registered tab has unsaved changes. */
  isAnyDirty(): boolean {
    for (const handle of this.tabs.values()) {
      if (handle.isDirty()) return true;
    }
    return false;
  }

  /** True if a specific tab has unsaved changes. */
  isDirty(tabId: string): boolean {
    return this.tabs.get(tabId)?.isDirty() ?? false;
  }

  /** Save all dirty tabs sequentially. Throws on first error. */
  async saveAll(): Promise<void> {
    for (const [, handle] of this.tabs) {
      if (handle.isDirty()) {
        await handle.save();
      }
    }
  }

  /** Save only the specified tab. */
  async saveTab(tabId: string): Promise<void> {
    const handle = this.tabs.get(tabId);
    if (handle?.isDirty()) {
      await handle.save();
    }
  }

  /** Reset all tabs to last-saved state. */
  resetAll(): void {
    for (const handle of this.tabs.values()) {
      handle.reset?.();
    }
  }
}

// Singleton — one coordinator per Settings page instance.
export const settingsForm = new SettingsFormCoordinator();
