// P2pViewerCard 回归（2026-10-07）：真机浏览器实测卡片不渲染——轮询
// 200 后 DOM 仍无卡片。此处钉住可见性口径 + 数据到位后必须渲染。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, waitFor, cleanup } from '@testing-library/svelte';
import P2pViewerCard from '$lib/components/P2pViewerCard.svelte';

const mockStatus = vi.fn();
vi.mock('$lib/api/p2p', () => ({
  getP2pViewer: (...a) => mockStatus(...a),
}));

const mkStatus = (over = {}) => ({
  enabled: true,
  associated: true,
  online: true,
  clients: 0,
  viewer: null,
  viewers: [],
  events: [],
  associ_name: '小米手机 · 阿明',
  ...over,
});

const mkViewer = (over = {}) => ({
  self_addr: '198.51.100.21:40001', mode: 'relay', rtt_ms: 87,
  kbps_up: 24, kbps_down: 380, bytes_total: 25123456,
  app_version: '0.1.3-debug+6107', updated_at: new Date().toISOString(),
  stale: false,
  samples: [{ at: new Date().toISOString(), mode: 'relay', rtt_ms: 87, kbps_up: 24, kbps_down: 380 }],
  ...over,
});

describe('P2pViewerCard', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it('未启用或未关联 → 整卡不渲染（用户可见性裁定）', async () => {
    mockStatus.mockResolvedValue(mkStatus({ enabled: false }));
    const { container } = render(P2pViewerCard);
    await vi.advanceTimersByTimeAsync(50);
    expect(container.querySelector('.card')).toBeNull();

    mockStatus.mockResolvedValue(mkStatus({ associated: false }));
    await vi.advanceTimersByTimeAsync(20000);
    expect(container.querySelector('.card')).toBeNull();
  });

  it('已关联无上报 → 渲染等待态', async () => {
    mockStatus.mockResolvedValue(mkStatus());
    const { getByText } = render(P2pViewerCard);
    await vi.advanceTimersByTimeAsync(50);
    expect(getByText('Associated — waiting for the app…')).toBeTruthy();
  });

  it('有上报 → 渲染地址/延时/模式徽标', async () => {
    mockStatus.mockResolvedValue(mkStatus({
      viewer: mkViewer(),
      viewers: [mkViewer()],
    }));
    const { getByText } = render(P2pViewerCard);
    await vi.advanceTimersByTimeAsync(50);
    await waitFor(() => expect(getByText('198.51.100.21:40001')).toBeTruthy());
    expect(getByText('Relay')).toBeTruthy();
    expect(getByText('87 ms')).toBeTruthy();
  });

  it('多观众：每台 App 各自渲染 + 台数徽标（2026-10-09 需求）', async () => {
    mockStatus.mockResolvedValue(mkStatus({
      viewers: [
        mkViewer({ self_addr: '116.22.1.93:13365', mode: 'tunnel', rtt_ms: 46 }),
        mkViewer({ self_addr: '198.51.100.21:40001', mode: 'relay', rtt_ms: 232, stale: true }),
      ],
    }));
    const { getByText } = render(P2pViewerCard);
    await vi.advanceTimersByTimeAsync(50);
    await waitFor(() => expect(getByText('116.22.1.93:13365')).toBeTruthy());
    expect(getByText('198.51.100.21:40001')).toBeTruthy();
    expect(getByText('×2')).toBeTruthy();
    // stale 观众带「近 3 分钟无上报」警示。
    expect(getByText('No report in 3 min (app may be backgrounded)')).toBeTruthy();
  });

  it('连接记录：事件按最新在上渲染（仅已关联时整卡可见）', async () => {
    const now = Date.now();
    mockStatus.mockResolvedValue(mkStatus({
      events: [
        { at: new Date(now - 3000).toISOString(), type: 'online', detail: 'signaling connected' },
        { at: new Date(now - 2000).toISOString(), type: 'tunnel_up', detail: 'clients=1' },
        { at: new Date(now - 1000).toISOString(), type: 'tunnel_down', detail: 'active=0' },
      ],
      viewers: [mkViewer()],
    }));
    const { getByText, container } = render(P2pViewerCard);
    await vi.advanceTimersByTimeAsync(50);
    await waitFor(() => expect(getByText('Connection log')).toBeTruthy());
    const items = [...container.querySelectorAll('li')].map((li) => li.textContent);
    expect(items.length).toBe(3);
    // 最新在上。
    expect(items[0]).toContain('Tunnel disconnected');
    expect(items[2]).toContain('Signaling online');
  });
});
