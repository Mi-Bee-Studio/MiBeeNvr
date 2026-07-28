<script lang="ts">
  // Shared toggle/switch component. Replaces 10+ inline copies across the
  // Settings tabs with consistent a11y (keyboard, aria-checked, role="switch").
  interface Props {
    checked: boolean;
    onChange: (v: boolean) => void;
    disabled?: boolean;
    label?: string;
  }

  let { checked, onChange, disabled = false, label }: Props = $props();

  function handleClick() {
    if (!disabled) onChange(!checked);
  }
</script>

<button
  type="button"
  class="relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-offset-2 {disabled
    ? 'opacity-50 cursor-not-allowed'
    : 'cursor-pointer'} {checked ? 'bg-blue-600' : 'th-bg-tertiary'}"
  role="switch"
  aria-checked={checked}
  aria-label={label}
  disabled={disabled}
  onclick={handleClick}
>
  <span
    class="inline-block h-4 w-4 transform rounded-full bg-white transition-transform {checked
      ? 'translate-x-6'
      : 'translate-x-1'}"
  ></span>
</button>
