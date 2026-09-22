<script lang="ts">
  import { t } from '$lib/i18n';
  import { BrainCircuit } from 'lucide-svelte';
</script>

<div class="card p-4 border th-border mb-6">
  <h3 class="text-sm font-semibold th-text-primary flex items-center gap-2">
    <BrainCircuit size={16} class="text-accent" />
    {t('ai.title')}
  </h3>
  <p class="text-sm th-text-muted mt-2">{t('ai.browserOnly')}</p>
</div>
