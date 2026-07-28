import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';
import Toggle from './Toggle.svelte';

afterEach(() => cleanup());

describe('Toggle', () => {
  it('renders unchecked state', () => {
    const { getByRole } = render(Toggle, { checked: false, onChange: () => {} });
    const sw = getByRole('switch');
    expect(sw.getAttribute('aria-checked')).toBe('false');
  });

  it('renders checked state', () => {
    const { getByRole } = render(Toggle, { checked: true, onChange: () => {} });
    const sw = getByRole('switch');
    expect(sw.getAttribute('aria-checked')).toBe('true');
  });

  it('fires onChange with new value on click', async () => {
    const onChange = vi.fn();
    const { getByRole } = render(Toggle, { checked: false, onChange });
    await fireEvent.click(getByRole('switch'));
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it('toggles back to false on second click', async () => {
    const onChange = vi.fn();
    const { getByRole } = render(Toggle, { checked: true, onChange });
    await fireEvent.click(getByRole('switch'));
    expect(onChange).toHaveBeenCalledWith(false);
  });

  it('does not fire onChange when disabled', async () => {
    const onChange = vi.fn();
    const { getByRole } = render(Toggle, { checked: false, onChange, disabled: true });
    await fireEvent.click(getByRole('switch'));
    expect(onChange).not.toHaveBeenCalled();
  });

  it('sets aria-label from label prop', () => {
    const { getByRole } = render(Toggle, { checked: false, onChange: () => {}, label: 'Enable AI' });
    expect(getByRole('switch').getAttribute('aria-label')).toBe('Enable AI');
  });

  it('has tabindex for keyboard focus', () => {
    const { getByRole } = render(Toggle, { checked: false, onChange: () => {} });
    const sw = getByRole('switch') as HTMLButtonElement;
    // <button> is focusable by default (tabindex 0 via native semantics)
    expect(sw.tagName).toBe('BUTTON');
  });
});
