import { render, fireEvent, cleanup } from '@testing-library/svelte';
import { describe, it, expect, afterEach } from 'vitest';
import SettingsCard from '../SettingsCard.svelte';

afterEach(cleanup);

describe('SettingsCard accordion', () => {
  it('toggles open on header click', async () => {
    const { container } = render(SettingsCard, { title: 'T', subtitle: 'S' });
    const btn = container.querySelector('button[aria-expanded]');
    expect(btn).toBeTruthy();
    expect(btn.getAttribute('aria-expanded')).toBe('false');
    await fireEvent.click(btn);
    expect(btn.getAttribute('aria-expanded')).toBe('true');
  });

  it('mounts the content wrapper only when open', async () => {
    const { container } = render(SettingsCard, { title: 'T' });
    expect(container.querySelector('.card div.px-6')).toBeNull();
    const btn = container.querySelector('button[aria-expanded]');
    await fireEvent.click(btn);
    expect(container.querySelector('.card div.px-6')).not.toBeNull();
  });
});
