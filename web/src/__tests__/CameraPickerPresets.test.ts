import { render, fireEvent, cleanup } from '@testing-library/svelte';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import CameraPickerPanel from '$lib/components/surveillance/CameraPickerPanel.svelte';
import {
  WALL_PRESET_STORAGE_KEY, MAX_WALL_PRESETS,
  type WallPreset,
} from '$lib/wall-presets';

vi.mock('lucide-svelte', () => ({
  Search: () => null, X: () => null, Plus: () => null,
  Settings: () => null, Save: () => null, Trash2: () => null,
}));

const cameras = [
  { id: 'cam-1', name: 'Front Door', protocol: 'rtsp', status: 'recording', group: '' },
  { id: 'cam-2', name: 'Back Yard', protocol: 'onvif', status: 'recording', group: '' },
] as any;

describe('CameraPickerPanel wall presets (max 3)', () => {
  const baseProps = () => ({
    allCameras: cameras,
    registryGroups: [],
    effectiveMax: 9,
    serverBudget: 20,
    gridCapacityOptions: [4, 9, 16],
    maxGridCameras: 4,
    photoWall: false,
    rotationSecs: 0,
    wallTemplateId: 'hero',
    pendingCameraIds: ['cam-1'],
    onapply: vi.fn(),
    onchangecapacity: vi.fn(),
    onloadpreset: vi.fn(),
    ondismiss: vi.fn(),
  });

  beforeEach(() => localStorage.clear());
  afterEach(() => cleanup());

  function seedPresets(list: WallPreset[]) {
    localStorage.setItem(WALL_PRESET_STORAGE_KEY, JSON.stringify(list));
  }

  const preset = (i: number): WallPreset => ({
    id: `p${i}`, name: `组合${i}`, cameraIds: ['cam-2'], maxGridCameras: 4,
    photoWall: false, rotationSecs: 0, wallTemplateId: 'hero', savedAt: i,
  });

  it('renders the presets section with an empty hint initially', () => {
    const { getByText } = render(CameraPickerPanel, { props: baseProps() });
    expect(getByText(/组合方案|Saved walls/)).toBeTruthy();
    expect(getByText(/还没有保存的组合方案|No saved walls yet/)).toBeTruthy();
  });

  it('saves the current selection as a preset and shows its chip', async () => {
    const { getByPlaceholderText, getAllByRole } = render(CameraPickerPanel, { props: baseProps() });
    const nameInput = getByPlaceholderText(/方案名称|Wall name/);
    await fireEvent.input(nameInput, { target: { value: '大门' } });
    const saveBtn = getAllByRole('button').find((b) => /保存当前|Save current/.test(b.textContent || ''))!;
    await fireEvent.click(saveBtn);
    const stored = JSON.parse(localStorage.getItem(WALL_PRESET_STORAGE_KEY)!);
    expect(stored).toHaveLength(1);
    expect(stored[0].name).toBe('大门');
    expect(stored[0].cameraIds).toEqual(['cam-1']);
  });

  it('loads a saved preset through the callback', async () => {
    seedPresets([preset(1)]);
    const props = baseProps();
    const { getAllByRole } = render(CameraPickerPanel, { props });
    const loadBtn = getAllByRole('button').find((b) => b.textContent?.trim() === '组合1')!;
    await fireEvent.click(loadBtn);
    expect(props.onloadpreset).toHaveBeenCalledTimes(1);
    expect((props.onloadpreset as any).mock.calls[0][0].name).toBe('组合1');
  });

  it('deletes a preset', async () => {
    seedPresets([preset(1)]);
    const { getAllByRole } = render(CameraPickerPanel, { props: baseProps() });
    const delBtn = getAllByRole('button').find((b) =>
      /删除方案|Delete wall/.test(b.getAttribute('aria-label') || b.getAttribute('title') || '')
    )!;
    await fireEvent.click(delBtn);
    expect(JSON.parse(localStorage.getItem(WALL_PRESET_STORAGE_KEY)!)).toEqual([]);
  });

  it('shows the count against the 3 cap', () => {
    seedPresets([preset(1), preset(2), preset(3)]);
    const { getByText } = render(CameraPickerPanel, { props: baseProps() });
    expect(getByText(`3 / ${MAX_WALL_PRESETS}`)).toBeTruthy();
  });
});
