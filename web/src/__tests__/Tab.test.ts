import { render, fireEvent, cleanup } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import Tab from '$lib/components/Tab.svelte';

describe('Tab', () => {
  afterEach(() => cleanup());

  const tabs = [
    { id: 'active', label: 'Active' },
    { id: 'archived', label: 'Archived' },
  ];

  it('renders all tab labels', () => {
    const { getByText } = render(Tab, { props: { tabs, activeTab: 'active', onchange: vi.fn() } });
    expect(getByText('Active')).toBeTruthy();
    expect(getByText('Archived')).toBeTruthy();
  });

  it('fires onchange when tab is clicked', async () => {
    const onchange = vi.fn();
    const { getByText } = render(Tab, { props: { tabs, activeTab: 'active', onchange } });
    await fireEvent.click(getByText('Archived'));
    expect(onchange).toHaveBeenCalledWith('archived');
  });

  it('shows count badge when count is provided', () => {
    const tabsWithCount = [
      { id: 'active', label: 'Active', count: 5 },
      { id: 'archived', label: 'Archived', count: 0 },
    ];
    const { getByText } = render(Tab, {
      props: { tabs: tabsWithCount, activeTab: 'active', onchange: vi.fn() },
    });
    expect(getByText('5')).toBeTruthy();
  });
});
