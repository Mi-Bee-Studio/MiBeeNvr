<script lang="ts">
  // GB28181 non-PTZ device controls (#379): remote record, arm/disarm, alarm
  // reset, home position, reboot (double-confirm).
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { sendGB28181DeviceControl } from '$lib/api';
  import { CircleDot, ShieldCheck, ShieldOff, BellOff, Home, Power, Disc } from 'lucide-svelte';

  let { channelId }: { channelId: string } = $props();
  let busy = $state('');

  async function run(command: string, label: string, confirmText?: string) {
    if (confirmText && !window.confirm(confirmText)) return;
    busy = command;
    try {
      await sendGB28181DeviceControl(channelId, command, !!confirmText);
      showToast(label + ' — ' + t('gb28181.control.sent'), 'success');
    } catch (e: any) {
      showToast(e.message || t('gb28181.control.failed'), 'error');
    } finally {
      busy = '';
    }
  }
</script>

<div class="card border th-border p-4">
  <div class="text-sm font-medium th-text-primary mb-3">{t('gb28181.control.title')}</div>
  <div class="flex flex-wrap gap-2">
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => run('record', t('gb28181.control.record'))}>
      <Disc size={14} /> {t('gb28181.control.record')}
    </button>
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => run('stop_record', t('gb28181.control.stopRecord'))}>
      <CircleDot size={14} /> {t('gb28181.control.stopRecord')}
    </button>
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => run('set_guard', t('gb28181.control.setGuard'))}>
      <ShieldCheck size={14} /> {t('gb28181.control.setGuard')}
    </button>
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => run('reset_guard', t('gb28181.control.resetGuard'))}>
      <ShieldOff size={14} /> {t('gb28181.control.resetGuard')}
    </button>
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => run('reset_alarm', t('gb28181.control.resetAlarm'))}>
      <BellOff size={14} /> {t('gb28181.control.resetAlarm')}
    </button>
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => run('home_position', t('gb28181.control.homePosition'))}>
      <Home size={14} /> {t('gb28181.control.homePosition')}
    </button>
    <button class="btn btn-danger btn-sm" disabled={!!busy}
      onclick={() => run('tele_boot', t('gb28181.control.reboot'), t('gb28181.control.confirmReboot'))}>
      <Power size={14} /> {t('gb28181.control.reboot')}
    </button>
  </div>
</div>
