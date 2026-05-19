import { render, fireEvent, cleanup } from '@testing-library/svelte';
import { describe, it, expect, vi, afterEach } from 'vitest';
import ArchiveConfirmDialog from '$lib/components/ArchiveConfirmDialog.svelte';

// Mock lucide-svelte
vi.mock('lucide-svelte', () => ({
  AlertTriangle: function MockIcon() {
    return document.createElement('span');
  },
}));

describe('ArchiveConfirmDialog', () => {
  afterEach(() => cleanup());

  const defaultProps = {
    cameraName: 'Front Door',
    recordingCount: 10,
    totalSize: '1.5 GB',
    onconfirm: vi.fn(),
    oncancel: vi.fn(),
  };

  it('renders camera name in dialog', () => {
    const { getByText } = render(ArchiveConfirmDialog, { props: defaultProps });
    // The i18n message should include the camera name
    expect(getByText(/Front Door/)).toBeTruthy();
  });

  it('calls oncancel when cancel button is clicked in step 1', async () => {
    const oncancel = vi.fn();
    const { getAllByRole } = render(ArchiveConfirmDialog, {
      props: { ...defaultProps, oncancel },
    });
    const cancelBtns = getAllByRole('button').filter(b => b.textContent?.includes('Cancel'));
    await fireEvent.click(cancelBtns[0]);
    expect(oncancel).toHaveBeenCalled();
  });

  it('advances to step 2 when Continue is clicked', async () => {
    const { getByRole, getByPlaceholderText } = render(ArchiveConfirmDialog, {
      props: defaultProps,
    });
    await fireEvent.click(getByRole('button', { name: 'Continue' }));
    expect(getByPlaceholderText('DELETE')).toBeTruthy();
  });

  it('confirm button is disabled until DELETE is typed', async () => {
    const { getByRole, getByPlaceholderText } = render(ArchiveConfirmDialog, {
      props: defaultProps,
    });
    await fireEvent.click(getByRole('button', { name: 'Continue' }));

    const confirmBtn = getByRole('button', { name: 'Confirm Archive' });
    expect((confirmBtn as HTMLButtonElement).disabled).toBe(true);

    const input = getByPlaceholderText('DELETE');
    await fireEvent.input(input, { target: { value: 'DELETE' } });
    expect((confirmBtn as HTMLButtonElement).disabled).toBe(false);
  });
});
