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
	
	toasts.update(current => [...current, { id, message, type }]);
	
	// Auto-dismiss after 3 seconds
	setTimeout(() => {
		toasts.update(current => current.filter(toast => toast.id !== id));
	}, 3000);
}

export function dismissToast(id: string) {
	toasts.update(current => current.filter(toast => toast.id !== id));
}