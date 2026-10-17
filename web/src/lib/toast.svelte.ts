export interface Toast {
  id: string;
  message: string;
  type: 'success' | 'error' | 'info' | 'warning';
}

// Module-level $state: the single toast list, reactive across every importer
// without svelte/store (this retires the last writable store in the codebase).
let toasts = $state<Toast[]>([]);

// Getter preserves $state reactivity for list consumers (the Toast
// component's each block) while the variable itself stays unexported.
export function getToasts(): Toast[] {
  return toasts;
}

let idCounter = 0;

export function showToast(message: string, type: 'success' | 'error' | 'info' | 'warning' = 'info') {
  const id = `toast-${++idCounter}`;

  toasts = [...toasts, { id, message, type }];

  // Auto-dismiss timing scales with severity: errors/warnings carry diagnostic
  // detail the user needs to read and act on, so they persist longer than the
  // transient success/info toasts. A fixed 3s for everything meant errors often
  // vanished before the user finished reading them.
  const duration = type === 'error' || type === 'warning' ? 6000 : 3000;
  setTimeout(() => {
    toasts = toasts.filter((toast) => toast.id !== id);
  }, duration);
}

export function dismissToast(id: string) {
  toasts = toasts.filter((toast) => toast.id !== id);
}
