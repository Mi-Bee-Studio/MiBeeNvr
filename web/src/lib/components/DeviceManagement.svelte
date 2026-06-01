<script lang="ts">
  import { onMount } from 'svelte';
  import { t } from '$lib/i18n';
  import {
    rebootDevice,
    getNetworkInterfaces,
    setNetworkInterfaces,
    getDeviceUsers,
    createDeviceUsers,
    deleteDeviceUsers,
  } from '$lib/api';
  import type { ONVIFNetworkInterface, ONVIFDeviceUser } from '$lib/api';
  import { showToast } from '$lib/toast';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import {
    Wifi,
    Users,
    Power,
    RefreshCw,
    Plus,
    Trash2,
    AlertTriangle,
    Save,
    ChevronDown,
  } from 'lucide-svelte';

  interface Props {
    cameraId: string;
    cameraName?: string;
  }

  let { cameraId, cameraName = '' }: Props = $props();

  // Loading states
  let networkLoading = $state(true);
  let usersLoading = $state(true);
  let networkSaving = $state(false);
  let rebooting = $state(false);
  let addingUser = $state(false);

  // Error states (501 = unsupported)
  let networkError = $state('');
  let usersError = $state('');
  let networkUnsupported = $state(false);
  let usersUnsupported = $state(false);
  let rebootUnsupported = $state(false);

  // Data
  let interfaces = $state<ONVIFNetworkInterface[]>([]);
  let editInterfaces = $state<ONVIFNetworkInterface[]>([]);
  let users = $state<ONVIFDeviceUser[]>([]);

  // Network edit mode
  let networkEditing = $state(false);

  // Add user form
  let newUserUsername = $state('');
  let newUserPassword = $state('');
  let newUserLevel = $state('User');

  // Confirmation dialogs
  let confirmDialog = $state<{
    title: string;
    message: string;
    onconfirm: () => void;
    variant: 'danger' | 'primary';
  } | null>(null);

  async function loadNetwork() {
    networkLoading = true;
    networkError = '';
    networkUnsupported = false;
    try {
      const result = await getNetworkInterfaces(cameraId);
      interfaces = result.interfaces || [];
      editInterfaces = JSON.parse(JSON.stringify(interfaces));
    } catch (e: any) {
      if (e?.message?.includes('not supported') || e?.message?.includes('501')) {
        networkUnsupported = true;
      } else {
        networkError = e instanceof Error ? e.message : String(e);
      }
    } finally {
      networkLoading = false;
    }
  }

  async function loadUsers() {
    usersLoading = true;
    usersError = '';
    usersUnsupported = false;
    try {
      const result = await getDeviceUsers(cameraId);
      users = result.users || [];
    } catch (e: any) {
      if (e?.message?.includes('not supported') || e?.message?.includes('501')) {
        usersUnsupported = true;
      } else {
        usersError = e instanceof Error ? e.message : String(e);
      }
    } finally {
      usersLoading = false;
    }
  }

  async function handleSaveNetwork() {
    networkSaving = true;
    try {
      await setNetworkInterfaces(cameraId, editInterfaces);
      interfaces = JSON.parse(JSON.stringify(editInterfaces));
      networkEditing = false;
      showToast(t('onvif.device.network.saved'), 'success');
    } catch (e: any) {
      showToast(t('onvif.device.error'), 'error');
    } finally {
      networkSaving = false;
    }
  }

  function cancelNetworkEdit() {
    editInterfaces = JSON.parse(JSON.stringify(interfaces));
    networkEditing = false;
  }

  async function handleReboot() {
    confirmDialog = {
      title: t('onvif.device.rebootConfirm').replace('{name}', cameraName || cameraId),
      message: t('onvif.device.rebootConfirmDesc'),
      variant: 'danger',
      onconfirm: async () => {
        rebooting = true;
        confirmDialog = null;
        try {
          await rebootDevice(cameraId);
          showToast(t('onvif.device.rebootSuccess'), 'success');
        } catch (e: any) {
          if (e?.message?.includes('not supported') || e?.message?.includes('501')) {
            rebootUnsupported = true;
            showToast(t('onvif.device.rebootUnsupported'), 'error');
          } else {
            showToast(t('onvif.device.error'), 'error');
          }
        } finally {
          rebooting = false;
        }
      },
    };
  }

  function handleDeleteUser(username: string) {
    confirmDialog = {
      title: t('onvif.device.users.confirmDelete').replace('{username}', username),
      message: t('onvif.device.users.confirmDeleteDesc'),
      variant: 'danger',
      onconfirm: async () => {
        confirmDialog = null;
        try {
          await deleteDeviceUsers(cameraId, [username]);
          showToast(t('onvif.device.users.deleted'), 'success');
          await loadUsers();
        } catch (e: any) {
          showToast(t('onvif.device.error'), 'error');
        }
      },
    };
  }

  async function handleAddUser() {
    if (!newUserUsername.trim()) return;
    addingUser = true;
    try {
      const user: ONVIFDeviceUser = {
        username: newUserUsername.trim(),
        level: newUserLevel,
      };
      if (newUserPassword) user.password = newUserPassword;
      await createDeviceUsers(cameraId, [user]);
      showToast(t('onvif.device.users.added'), 'success');
      newUserUsername = '';
      newUserPassword = '';
      newUserLevel = 'User';
      await loadUsers();
    } catch (e: any) {
      showToast(t('onvif.device.error'), 'error');
    } finally {
      addingUser = false;
    }
  }

  function formatLevel(level: string): string {
    const key = `onvif.device.users.${level.toLowerCase()}`;
    const translated = t(key);
    // If no translation found, return original
    return translated === key ? level : translated;
  }

  onMount(() => {
    loadNetwork();
    loadUsers();
  });
</script>

<div class="device-mgmt">
  <!-- Network Section -->
  <details class="device-mgmt-section" open>
    <summary class="device-mgmt-summary">
      <Wifi size={16} />
      <span>{t('onvif.device.network')}</span>
      {#if networkUnsupported}
        <span class="device-mgmt-unsupported">{t('onvif.device.network.unsupported')}</span>
      {/if}
    </summary>

    <div class="device-mgmt-body">
      {#if networkLoading}
        <div class="device-mgmt-loading">
          <span class="spinner"></span>
          {t('onvif.device.loading')}
        </div>
      {:else if networkUnsupported}
        <div class="device-mgmt-unsupported-detail">
          <AlertTriangle size={16} />
          <span>{t('onvif.device.network.unsupported')}</span>
        </div>
      {:else if networkError}
        <div class="device-mgmt-error">
          <span>{networkError}</span>
          <button class="btn btn-ghost btn-sm" onclick={loadNetwork}>
            <RefreshCw size={14} />
            {t('common.retry')}
          </button>
        </div>
      {:else if interfaces.length === 0}
        <div class="device-mgmt-empty">{t('onvif.device.network.unsupported')}</div>
      {:else}
        {#if networkEditing}
          {#each editInterfaces as iface, i (i)}
            <div class="device-mgmt-iface">
              <div class="device-mgmt-iface-header">
                <span class="device-mgmt-iface-name">{iface.name}</span>
                <label class="device-mgmt-toggle">
                  <input
                    type="checkbox"
                    class="accent-[var(--color-accent)]"
                    bind:checked={iface.enabled}
                  />
                  <span class="device-mgmt-toggle-label">{t('onvif.device.network.enabled')}</span>
                </label>
              </div>
              <div class="device-mgmt-iface-grid">
                <div>
                  <label class="device-mgmt-field-label" for="dm-dhcp">{t('onvif.device.network.dhcp')}</label>
                  <label class="device-mgmt-toggle-sm">
                    <input
                      type="checkbox"
                      class="accent-[var(--color-accent)]"
                      bind:checked={iface.ipv4.dhcp}
                    />
                    <span>DHCP</span>
                  </label>
                </div>
                <div>
                  <label class="device-mgmt-field-label" for="dm-address">{t('onvif.device.network.address')}</label>
                  <input
                    type="text"
                    class="input input-sm"
                    bind:value={iface.ipv4.address}
                    disabled={iface.ipv4.dhcp}
                    placeholder="192.168.1.100"
                  />
                </div>
                <div>
                  <label class="device-mgmt-field-label" for="dm-netmask">{t('onvif.device.network.netmask')}</label>
                  <input
                    type="text"
                    class="input input-sm"
                    bind:value={iface.ipv4.netmask}
                    disabled={iface.ipv4.dhcp}
                    placeholder="255.255.255.0"
                  />
                </div>
                <div>
                  <label class="device-mgmt-field-label" for="dm-gateway">{t('onvif.device.network.gateway')}</label>
                  <input
                    type="text"
                    class="input input-sm"
                    bind:value={iface.ipv4.gateway}
                    disabled={iface.ipv4.dhcp}
                    placeholder="192.168.1.1"
                  />
                </div>
              </div>
            </div>
          {/each}
          <div class="device-mgmt-actions">
            <button
              class="btn btn-primary btn-sm"
              onclick={handleSaveNetwork}
              disabled={networkSaving}
            >
              {#if networkSaving}
                <span class="spinner mr-1"></span>
              {:else}
                <Save size={14} />
              {/if}
              {networkSaving ? t('onvif.device.network.saving') : t('onvif.device.network.save')}
            </button>
            <button class="btn btn-ghost btn-sm" onclick={cancelNetworkEdit}>
              {t('common.cancel')}
            </button>
          </div>
        {:else}
          {#each interfaces as iface (iface.name)}
            <div class="device-mgmt-iface">
              <div class="device-mgmt-iface-header">
                <span class="device-mgmt-iface-name">{iface.name}</span>
                <span class="device-mgmt-iface-status" class:device-mgmt-iface-on={iface.enabled}>
                  {iface.enabled ? t('onvif.device.network.enabled') : 'Off'}
                </span>
              </div>
              <div class="device-mgmt-iface-grid">
                <div class="device-mgmt-field">
                  <span class="device-mgmt-field-label">{t('onvif.device.network.address')}</span>
                  <span class="device-mgmt-field-value">
                    {iface.ipv4.address || '—'}
                    {#if iface.ipv4.dhcp}
                      <span class="device-mgmt-dhcp-badge">DHCP</span>
                    {/if}
                  </span>
                </div>
                <div class="device-mgmt-field">
                  <span class="device-mgmt-field-label">{t('onvif.device.network.netmask')}</span>
                  <span class="device-mgmt-field-value">{iface.ipv4.netmask || '—'}</span>
                </div>
                <div class="device-mgmt-field">
                  <span class="device-mgmt-field-label">{t('onvif.device.network.gateway')}</span>
                  <span class="device-mgmt-field-value">{iface.ipv4.gateway || '—'}</span>
                </div>
                {#if iface.dns && iface.dns.length > 0}
                  <div class="device-mgmt-field">
                    <span class="device-mgmt-field-label">{t('onvif.device.network.dns')}</span>
                    <span class="device-mgmt-field-value">{iface.dns.join(', ')}</span>
                  </div>
                {/if}
              </div>
            </div>
          {/each}
          <button class="btn btn-ghost btn-sm mt-2" onclick={() => networkEditing = true}>
            {t('common.edit')}
          </button>
        {/if}
      {/if}
    </div>
  </details>

  <!-- Users Section -->
  <details class="device-mgmt-section" open>
    <summary class="device-mgmt-summary">
      <Users size={16} />
      <span>{t('onvif.device.users')}</span>
      {#if usersUnsupported}
        <span class="device-mgmt-unsupported">{t('onvif.device.users.unsupported')}</span>
      {/if}
    </summary>

    <div class="device-mgmt-body">
      {#if usersLoading}
        <div class="device-mgmt-loading">
          <span class="spinner"></span>
          {t('onvif.device.loading')}
        </div>
      {:else if usersUnsupported}
        <div class="device-mgmt-unsupported-detail">
          <AlertTriangle size={16} />
          <span>{t('onvif.device.users.unsupported')}</span>
        </div>
      {:else if usersError}
        <div class="device-mgmt-error">
          <span>{usersError}</span>
          <button class="btn btn-ghost btn-sm" onclick={loadUsers}>
            <RefreshCw size={14} />
            {t('common.retry')}
          </button>
        </div>
      {:else}
        {#if users.length === 0}
          <div class="device-mgmt-empty">{t('onvif.device.users.noUsers')}</div>
        {:else}
          <div class="device-mgmt-users-table">
            <div class="device-mgmt-users-header">
              <span>{t('onvif.device.users.username')}</span>
              <span>{t('onvif.device.users.level')}</span>
              <span></span>
            </div>
            {#each users as user (user.username)}
              <div class="device-mgmt-user-row">
                <span class="device-mgmt-user-name">{user.username}</span>
                <span class="device-mgmt-user-level">{formatLevel(user.level)}</span>
                <button
                  class="btn btn-ghost btn-sm device-mgmt-user-delete"
                  onclick={() => handleDeleteUser(user.username)}
                  title={t('onvif.device.users.deleteUser')}
                >
                  <Trash2 size={14} />
                </button>
              </div>
            {/each}
          </div>
        {/if}

        <!-- Add user form -->
        <div class="device-mgmt-add-user">
          <div class="device-mgmt-add-user-title">
            <Plus size={14} />
            {t('onvif.device.users.addUser')}
          </div>
          <div class="device-mgmt-add-user-form">
            <input
              type="text"
              class="input input-sm"
              bind:value={newUserUsername}
              placeholder={t('onvif.device.users.username')}
            />
            <input
              type="password"
              class="input input-sm"
              bind:value={newUserPassword}
              placeholder={t('onvif.device.users.username') === '用户名' ? '密码' : 'Password'}
            />
            <select class="input input-sm" bind:value={newUserLevel}>
              <option value="Administrator">{t('onvif.device.users.administrator')}</option>
              <option value="Operator">{t('onvif.device.users.operator')}</option>
              <option value="User">{t('onvif.device.users.user')}</option>
            </select>
            <button
              class="btn btn-primary btn-sm"
              onclick={handleAddUser}
              disabled={addingUser || !newUserUsername.trim()}
            >
              {#if addingUser}
                <span class="spinner mr-1"></span>
              {:else}
                <Plus size={14} />
              {/if}
              {t('onvif.device.users.addUser')}
            </button>
          </div>
        </div>
      {/if}
    </div>
  </details>

  <!-- System Section -->
  <details class="device-mgmt-section">
    <summary class="device-mgmt-summary">
      <Power size={16} />
      <span>{t('onvif.device.system')}</span>
    </summary>

    <div class="device-mgmt-body">
      {#if rebootUnsupported}
        <div class="device-mgmt-unsupported-detail">
          <AlertTriangle size={16} />
          <span>{t('onvif.device.rebootUnsupported')}</span>
        </div>
      {:else}
        <div class="device-mgmt-reboot">
          <div class="device-mgmt-reboot-info">
            <p class="device-mgmt-reboot-desc">{t('onvif.device.rebootDesc')}</p>
          </div>
          <button
            class="btn btn-sm device-mgmt-reboot-btn"
            onclick={handleReboot}
            disabled={rebooting}
          >
            {#if rebooting}
              <span class="spinner mr-1"></span>
              {t('onvif.device.rebooting')}
            {:else}
              <Power size={14} />
              {t('onvif.device.reboot')}
            {/if}
          </button>
        </div>
      {/if}
    </div>
  </details>
</div>

{#if confirmDialog}
  <ConfirmDialog
    title={confirmDialog.title}
    message={confirmDialog.message}
    variant={confirmDialog.variant}
    onconfirm={confirmDialog.onconfirm}
    oncancel={() => confirmDialog = null}
  />
{/if}

<style>
  .device-mgmt {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .device-mgmt-section {
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    overflow: hidden;
    background-color: var(--bg-elevated);
  }

  .device-mgmt-section[open] {
    border-color: var(--border-hover);
  }

  .device-mgmt-summary {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.75rem 1rem;
    cursor: pointer;
    font-size: 0.8125rem;
    font-weight: 600;
    color: var(--text-primary);
    background-color: var(--bg-secondary);
    user-select: none;
    transition: background-color var(--duration-fast) var(--ease-out);
    list-style: none;
  }

  .device-mgmt-summary::-webkit-details-marker {
    display: none;
  }

  .device-mgmt-summary::before {
    content: '';
    display: inline-block;
    width: 0;
    height: 0;
    border-left: 5px solid var(--text-tertiary);
    border-top: 4px solid transparent;
    border-bottom: 4px solid transparent;
    margin-right: 0.25rem;
    transition: transform var(--duration-fast) var(--ease-out);
  }

  .device-mgmt-section[open] > .device-mgmt-summary::before {
    transform: rotate(90deg);
  }

  .device-mgmt-summary:hover {
    background-color: var(--bg-hover);
  }

  .device-mgmt-unsupported {
    font-size: 0.6875rem;
    font-weight: 400;
    color: var(--text-tertiary);
    margin-left: auto;
  }

  .device-mgmt-body {
    padding: 1rem;
  }

  .device-mgmt-loading {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.8125rem;
    color: var(--text-secondary);
    padding: 0.5rem 0;
  }

  .device-mgmt-error {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    font-size: 0.8125rem;
    color: var(--color-danger);
    padding: 0.5rem 0;
  }

  .device-mgmt-empty {
    font-size: 0.8125rem;
    color: var(--text-tertiary);
    text-align: center;
    padding: 1rem 0;
  }

  .device-mgmt-unsupported-detail {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.8125rem;
    color: var(--text-tertiary);
    padding: 0.5rem 0;
  }

  /* Network interface card */
  .device-mgmt-iface {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 0.75rem;
    margin-bottom: 0.5rem;
    background-color: var(--bg-tertiary);
  }

  .device-mgmt-iface:last-child {
    margin-bottom: 0;
  }

  .device-mgmt-iface-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 0.5rem;
  }

  .device-mgmt-iface-name {
    font-size: 0.8125rem;
    font-weight: 600;
    color: var(--text-primary);
    font-family: monospace;
  }

  .device-mgmt-iface-status {
    font-size: 0.6875rem;
    font-weight: 500;
    color: var(--text-tertiary);
    padding: 0.125rem 0.5rem;
    border-radius: 9999px;
    background-color: var(--bg-hover);
  }

  .device-mgmt-iface-on {
    color: var(--color-success);
    background-color: rgba(16, 185, 129, 0.1);
  }

  .device-mgmt-iface-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
    gap: 0.5rem;
  }

  .device-mgmt-field {
    display: flex;
    flex-direction: column;
    gap: 0.125rem;
  }

  .device-mgmt-field-label {
    font-size: 0.6875rem;
    font-weight: 500;
    color: var(--text-tertiary);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }

  .device-mgmt-field-value {
    font-size: 0.8125rem;
    color: var(--text-primary);
    display: flex;
    align-items: center;
    gap: 0.375rem;
  }

  .device-mgmt-dhcp-badge {
    font-size: 0.5625rem;
    font-weight: 600;
    color: var(--color-primary);
    background-color: rgba(139, 92, 246, 0.1);
    padding: 0.0625rem 0.375rem;
    border-radius: 9999px;
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }

  .device-mgmt-toggle {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    cursor: pointer;
  }

  .device-mgmt-toggle-label {
    font-size: 0.75rem;
    color: var(--text-secondary);
  }

  .device-mgmt-toggle-sm {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    font-size: 0.75rem;
    color: var(--text-secondary);
    cursor: pointer;
  }

  .input-sm {
    font-size: 0.75rem;
    padding: 0.25rem 0.5rem;
  }

  .device-mgmt-actions {
    display: flex;
    gap: 0.5rem;
    margin-top: 0.75rem;
  }

  /* Users table */
  .device-mgmt-users-table {
    width: 100%;
    margin-bottom: 1rem;
  }

  .device-mgmt-users-header {
    display: grid;
    grid-template-columns: 1fr 1fr 2.5rem;
    gap: 0.5rem;
    padding: 0.5rem 0.75rem;
    font-size: 0.6875rem;
    font-weight: 600;
    color: var(--text-tertiary);
    text-transform: uppercase;
    letter-spacing: 0.03em;
    border-bottom: 1px solid var(--border);
  }

  .device-mgmt-user-row {
    display: grid;
    grid-template-columns: 1fr 1fr 2.5rem;
    gap: 0.5rem;
    padding: 0.5rem 0.75rem;
    align-items: center;
    border-bottom: 1px solid var(--border);
    transition: background-color var(--duration-fast) var(--ease-out);
  }

  .device-mgmt-user-row:last-child {
    border-bottom: none;
  }

  .device-mgmt-user-row:hover {
    background-color: var(--bg-hover);
  }

  .device-mgmt-user-name {
    font-size: 0.8125rem;
    font-weight: 500;
    color: var(--text-primary);
    font-family: monospace;
  }

  .device-mgmt-user-level {
    font-size: 0.75rem;
    color: var(--text-secondary);
  }

  .device-mgmt-user-delete {
    color: var(--text-tertiary);
    padding: 0.25rem;
    min-width: 1.75rem;
    min-height: 1.75rem;
  }

  .device-mgmt-user-delete:hover {
    color: var(--color-danger);
  }

  /* Add user form */
  .device-mgmt-add-user {
    border-top: 1px solid var(--border);
    padding-top: 0.75rem;
  }

  .device-mgmt-add-user-title {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    font-size: 0.75rem;
    font-weight: 600;
    color: var(--text-secondary);
    margin-bottom: 0.5rem;
  }

  .device-mgmt-add-user-form {
    display: flex;
    gap: 0.5rem;
    flex-wrap: wrap;
  }

  .device-mgmt-add-user-form .input {
    flex: 1;
    min-width: 8rem;
  }

  .device-mgmt-add-user-form select {
    flex: 0 0 auto;
    min-width: 8rem;
  }

  /* Reboot section */
  .device-mgmt-reboot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
  }

  .device-mgmt-reboot-info {
    flex: 1;
  }

  .device-mgmt-reboot-desc {
    font-size: 0.8125rem;
    color: var(--text-secondary);
    margin: 0;
  }

  .device-mgmt-reboot-btn {
    color: var(--color-danger);
    border-color: var(--color-danger);
    white-space: nowrap;
  }

  .device-mgmt-reboot-btn:hover {
    background-color: var(--color-danger);
    color: #ffffff;
  }
</style>
