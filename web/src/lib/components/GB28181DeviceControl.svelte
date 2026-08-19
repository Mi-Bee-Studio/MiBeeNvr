<script lang="ts">
  // GB28181 non-PTZ device controls (#379): remote record, arm/disarm, alarm
  // reset, home position, reboot (double-confirm). Plus FI lens control and
  // auxiliary switches (wiper/light, #341 — GB/T 28181-2022 § A.3.3/A.3.7).
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { sendGB28181DeviceControl, sendGB28181LensControl, sendGB28181AuxSwitch } from '$lib/api';
  import {
    CircleDot, ShieldCheck, ShieldOff, BellOff, Home, Power, Disc,
    Aperture, Focus, Square, Droplets, Lightbulb,
  } from 'lucide-svelte';

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

  async function runLens(action: string, label: string) {
    busy = 'lens:' + action;
    try {
      await sendGB28181LensControl(channelId, action);
      showToast(label + ' — ' + t('gb28181.control.sent'), 'success');
    } catch (e: any) {
      showToast(e.message || t('gb28181.control.failed'), 'error');
    } finally {
      busy = '';
    }
  }

  async function runAux(switchNo: number, on: boolean, label: string) {
    busy = 'aux:' + switchNo + ':' + on;
    try {
      await sendGB28181AuxSwitch(channelId, switchNo, on);
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

  <div class="text-sm font-medium th-text-primary mt-4 mb-2">{t('gb28181.control.lensTitle')}</div>
  <div class="flex flex-wrap gap-2">
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => runLens('iris-open', t('gb28181.control.irisOpen'))}>
      <Aperture size={14} /> {t('gb28181.control.irisOpen')}
    </button>
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => runLens('iris-close', t('gb28181.control.irisClose'))}>
      <Aperture size={14} /> {t('gb28181.control.irisClose')}
    </button>
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => runLens('focus-near', t('gb28181.control.focusNear'))}>
      <Focus size={14} /> {t('gb28181.control.focusNear')}
    </button>
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => runLens('focus-far', t('gb28181.control.focusFar'))}>
      <Focus size={14} /> {t('gb28181.control.focusFar')}
    </button>
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => runLens('stop', t('gb28181.control.lensStop'))}>
      <Square size={14} /> {t('gb28181.control.lensStop')}
    </button>
  </div>

  <div class="text-sm font-medium th-text-primary mt-4 mb-2">{t('gb28181.control.auxTitle')}</div>
  <div class="flex flex-wrap gap-2">
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => runAux(1, true, t('gb28181.control.wiperOn'))}>
      <Droplets size={14} /> {t('gb28181.control.wiperOn')}
    </button>
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => runAux(1, false, t('gb28181.control.wiperOff'))}>
      <Droplets size={14} /> {t('gb28181.control.wiperOff')}
    </button>
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => runAux(2, true, t('gb28181.control.lightOn'))}>
      <Lightbulb size={14} /> {t('gb28181.control.lightOn')}
    </button>
    <button class="btn btn-ghost btn-sm" disabled={!!busy}
      onclick={() => runAux(2, false, t('gb28181.control.lightOff'))}>
      <Lightbulb size={14} /> {t('gb28181.control.lightOff')}
    </button>
  </div>
</div>
