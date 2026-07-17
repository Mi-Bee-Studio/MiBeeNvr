import { writable } from 'svelte/store';

export interface Toast {
  id: string;
  message: string;
  type: 'success' | 'error' | 'info' | 'warning';
}

export const toasts = writable<Toast[]>([]);

let idCounter = 0;

export function showToast(message: string, type: 'success' | 'error' | 'info' | 'warning' = 'info') {
  const id = `toast-${++idCounter}`;

  toasts.update((current) => [...current, { id, message, type }]);

  // Auto-dismiss timing scales with severity: errors/warnings carry diagnostic
  // detail the user needs to read and act on, so they persist longer than the
  // transient success/info toasts. A fixed 3s for everything meant errors often
  // vanished before the user finished reading them.
  const duration = type === 'error' || type === 'warning' ? 6000 : 3000;
  setTimeout(() => {
    toasts.update((current) => current.filter((toast) => toast.id !== id));
  }, duration);
}

export function dismissToast(id: string) {
  toasts.update((current) => current.filter((toast) => toast.id !== id));
}
