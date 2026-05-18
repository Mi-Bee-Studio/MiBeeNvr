<script lang="ts">
  import { t } from '$lib/i18n';
  import { Camera, CheckCircle, ChevronRight, ChevronLeft, Video, Wifi } from 'lucide-svelte';

  interface Props {
    onaddcamera: () => void;
    oncomplete: () => void;
    onskip: () => void;
  }

  let { onaddcamera, oncomplete, onskip }: Props = $props();

  const DISMISSED_KEY = 'mibee_nvr_onboarding_dismissed';

  let currentStep = $state(0);
  let visible = $state(true);

  // Check if dismissed this session
  $effect(() => {
    if (sessionStorage.getItem(DISMISSED_KEY)) {
      visible = false;
    }
  });

  const steps = [
    { key: 'welcome', icon: Video },
    { key: 'camera', icon: Camera },
    { key: 'complete', icon: CheckCircle },
  ];

  function handleNext() {
    if (currentStep === 0) {
      currentStep = 1;
    } else if (currentStep === 1) {
      onaddcamera();
      currentStep = 2;
    } else {
      handleComplete();
    }
  }

  function handleBack() {
    if (currentStep > 0) currentStep--;
  }

  function handleSkip() {
    sessionStorage.setItem(DISMISSED_KEY, '1');
    visible = false;
    onskip();
  }

  function handleComplete() {
    sessionStorage.setItem(DISMISSED_KEY, '1');
    visible = false;
    oncomplete();
  }
</script>

{#if visible}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="overlay-backdrop" onkeydown={(e) => e.key === 'Escape' && handleSkip()}>
    <div class="overlay-card" role="dialog" aria-modal="true" aria-label={t('onboarding.welcome')}>
      <!-- Step indicator -->
      <div class="step-dots">
        {#each steps as step, i}
          <button
            class="dot"
            class:dot-active={i === currentStep}
            class:dot-done={i < currentStep}
            aria-label={t('onboarding.step', { current: i + 1, total: steps.length })}
          ></button>
        {/each}
      </div>

      <!-- Step content -->
      <div class="step-content">
        {#if currentStep === 0}
          <div class="icon-circle icon-circle-primary">
            <Video size={40} />
          </div>
          <h2 class="step-title">{t('onboarding.welcome')}</h2>
          <p class="step-desc">{t('onboarding.welcomeDesc')}</p>
        {:else if currentStep === 1}
          <div class="icon-circle icon-circle-accent">
            <Camera size={40} />
          </div>
          <h2 class="step-title">{t('onboarding.addCamera')}</h2>
          <p class="step-desc">{t('onboarding.addCameraDesc')}</p>
          <div class="supported-protocols">
            <span class="protocol-tag">RTSP</span>
            <span class="protocol-tag">HTTP JPEG</span>
            <span class="protocol-tag">ONVIF</span>
            <span class="protocol-tag">Xiaomi</span>
          </div>
        {:else}
          <div class="icon-circle icon-circle-success">
            <CheckCircle size={40} />
          </div>
          <h2 class="step-title">{t('onboarding.complete')}</h2>
          <p class="step-desc">{t('onboarding.completeDesc')}</p>
        {/if}
      </div>

      <!-- Actions -->
      <div class="step-actions">
        {#if currentStep > 0}
          <button class="btn btn-ghost" onclick={handleBack}>
            <ChevronLeft size={16} />
            {t('onboarding.back')}
          </button>
        {:else}
          <button class="btn btn-ghost" onclick={handleSkip}>
            {t('onboarding.skip')}
          </button>
        {/if}

        {#if currentStep < 2}
          <button class="btn btn-primary" onclick={handleNext}>
            {currentStep === 0 ? t('onboarding.getStarted') : t('onboarding.addCamera')}
            <ChevronRight size={16} />
          </button>
        {:else}
          <div class="flex gap-3">
            <button class="btn btn-secondary" onclick={onaddcamera}>
              {t('onboarding.addAnother')}
            </button>
            <button class="btn btn-primary" onclick={handleComplete}>
              {t('onboarding.goToRecordings')}
              <ChevronRight size={16} />
            </button>
          </div>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .overlay-backdrop {
    position: fixed;
    inset: 0;
    z-index: 2000;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(0, 0, 0, 0.6);
    backdrop-filter: blur(4px);
    -webkit-backdrop-filter: blur(4px);
    animation: fade-in 0.2s var(--ease-out);
  }

  .overlay-card {
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-lg);
    width: 100%;
    max-width: 440px;
    padding: 2.5rem 2rem 2rem;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1.5rem;
    animation: slide-up 0.3s var(--ease-out);
  }

  .step-dots {
    display: flex;
    gap: 0.5rem;
  }

  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    border: none;
    background: var(--border-hover);
    cursor: pointer;
    transition: all var(--duration-fast) var(--ease-out);
    padding: 0;
  }

  .dot-active {
    background: var(--color-primary);
    box-shadow: 0 0 0 3px rgba(139, 92, 246, 0.2);
    width: 24px;
    border-radius: 4px;
  }

  .dot-done {
    background: var(--color-success);
  }

  .step-content {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.75rem;
    text-align: center;
  }

  .icon-circle {
    width: 80px;
    height: 80px;
    border-radius: 50%;
    display: flex;
    align-items: center;
    justify-content: center;
    margin-bottom: 0.5rem;
  }

  .icon-circle-primary {
    background: rgba(139, 92, 246, 0.1);
    color: var(--color-primary);
  }

  .icon-circle-accent {
    background: rgba(56, 189, 248, 0.1);
    color: var(--color-accent);
  }

  .icon-circle-success {
    background: rgba(16, 185, 129, 0.1);
    color: var(--color-success);
  }

  .step-title {
    font-size: 1.5rem;
    font-weight: 700;
    color: var(--text-primary);
    margin: 0;
  }

  .step-desc {
    color: var(--text-secondary);
    font-size: 0.9375rem;
    line-height: 1.5;
    max-width: 340px;
    margin: 0;
  }

  .supported-protocols {
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem;
    justify-content: center;
    margin-top: 0.5rem;
  }

  .protocol-tag {
    display: inline-flex;
    align-items: center;
    padding: 0.25rem 0.625rem;
    border-radius: 9999px;
    font-size: 0.75rem;
    font-weight: 500;
    background: var(--bg-tertiary);
    color: var(--text-secondary);
    border: 1px solid var(--border);
  }

  .step-actions {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding-top: 1rem;
    border-top: 1px solid var(--border);
  }

  @keyframes fade-in {
    from { opacity: 0; }
    to { opacity: 1; }
  }

  @keyframes slide-up {
    from {
      opacity: 0;
      transform: translateY(16px) scale(0.98);
    }
    to {
      opacity: 1;
      transform: translateY(0) scale(1);
    }
  }
</style>
