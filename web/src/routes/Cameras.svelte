<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { listCameras, createCamera, updateCamera, deleteCamera } from '$lib/api';
  import type { Camera, CreateCameraRequest, UpdateCameraRequest } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';

  let cameras: Camera[] = [];
  let loading = true;
  let error = '';
  let feedback = '';
  let feedbackType: 'success' | 'error' = 'success';

  // Form state
  let showForm = false;
  let editingCamera: Camera | null = null;
  let formName = '';
  let formProtocol = 'rtsp_h264';
  let formUrl = '';
  let formUsername = '';
  let formPassword = '';
  let formEnabled = true;
  let saving = false;
  let validationErrors: Record<string, string> = {};

  // Delete confirmation
  let deletingCamera: Camera | null = null;

  function showFeedback(msg: string, type: 'success' | 'error') {
    showToast(msg, type);
  }

  function validate(): boolean {
    validationErrors = {};
    if (!formName.trim()) validationErrors['name'] = t('cameras.nameRequired');
    if (!formProtocol) validationErrors['protocol'] = t('cameras.protocolRequired');
    if (!formUrl.trim()) validationErrors['url'] = t('cameras.urlRequired');
    return Object.keys(validationErrors).length === 0;
  }

  function resetForm() {
    showForm = false;
    editingCamera = null;
    formName = '';
    formProtocol = 'rtsp_h264';
    formUrl = '';
    formUsername = '';
    formPassword = '';
    formEnabled = true;
    validationErrors = {};
  }

  function openAddForm() {
    resetForm();
    showForm = true;
  }

  function openEditForm(camera: Camera) {
    editingCamera = camera;
    formName = camera.name;
    formProtocol = camera.protocol;
    formUrl = camera.url;
    formUsername = '';
    formPassword = '';
    formEnabled = camera.enabled;
    validationErrors = {};
    showForm = true;
  }

  async function loadCameras() {
    loading = true;
    error = '';
    try {
      cameras = await listCameras();
    } catch (e) {
      error = e instanceof Error ? e.message : t('cameras.failedLoad');
    } finally {
      loading = false;
    }
  }

  async function handleSubmit() {
    if (!validate()) return;
    saving = true;

    try {
      if (editingCamera) {
        const data: UpdateCameraRequest = {
          name: formName,
          protocol: formProtocol,
          url: formUrl,
          enabled: formEnabled,
        };
        if (formUsername) data.username = formUsername;
        if (formPassword) data.password = formPassword;
        await updateCamera(editingCamera.id, data);
        showFeedback(t('cameras.cameraUpdated'), 'success');
      } else {
        const data: CreateCameraRequest = {
          name: formName,
          protocol: formProtocol,
          url: formUrl,
          enabled: formEnabled,
        };
        if (formUsername) data.username = formUsername;
        if (formPassword) data.password = formPassword;
        await createCamera(data);
        showFeedback(t('cameras.cameraAdded'), 'success');
      }
      resetForm();
      await loadCameras();
    } catch (e) {
      showFeedback(
        editingCamera ? t('cameras.failedUpdate') : t('cameras.failedAdd'),
        'error'
      );
    } finally {
      saving = false;
    }
  }

  async function handleDelete() {
    if (!deletingCamera) return;
    try {
      await deleteCamera(deletingCamera.id);
      showFeedback(t('cameras.cameraDeleted'), 'success');
      deletingCamera = null;
      await loadCameras();
    } catch (e) {
      showFeedback(t('cameras.failedDelete'), 'error');
      deletingCamera = null;
    }
  }

  onMount(() => {
    loadCameras();
  });
</script>

<div class="min-h-screen th-bg-primary pt-[68px]">

  <!-- Main content -->
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="flex items-center justify-between mb-6">
      <h2 class="text-2xl font-bold th-text-primary">{t('cameras.title')}</h2>
      <button on:click={openAddForm} class="btn btn-primary">
        + {t('cameras.addCamera')}
      </button>
    </div>

    <!-- Feedback -->
    {#if feedback}
      <div class="mb-4 p-3 rounded-md border {feedbackType === 'success'
        ? 'bg-[rgba(16,185,129,0.3)] border-[rgba(16,185,129,0.3)] th-color-success'
        : 'bg-[rgba(239,68,68,0.3)] th-border-danger th-color-danger'}">
        {feedback}
      </div>
    {/if}

    <!-- Error -->
    {#if error}
      <div class="mb-4 p-4 bg-[rgba(239,68,68,0.3)] border th-border-danger rounded-md th-color-danger">
        {error}
      </div>
    {/if}

    <!-- Loading -->
    {#if loading}
      <div class="flex justify-center items-center h-64">
        <div class="spinner spinner-lg"></div>
      </div>
    {:else}
      <div class="space-y-6">

        <!-- Add/Edit Form -->
        {#if showForm}
          <div class="card p-6 border th-border">
            <h3 class="text-lg font-semibold th-text-primary mb-4">
              {editingCamera ? t('cameras.editCamera') : t('cameras.addCamera')}
            </h3>

            <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
              <!-- Name -->
              <div>
                <label for="cam-name" class="input-label">{t('cameras.name')}</label>
                <input id="cam-name" type="text" class="input" bind:value={formName} />
                {#if validationErrors['name']}
                  <p class="th-color-danger text-xs mt-1">{validationErrors['name']}</p>
                {/if}
              </div>

              <!-- Protocol -->
              <div>
                <label for="cam-protocol" class="input-label">{t('cameras.protocol')}</label>
                <select id="cam-protocol" class="input" bind:value={formProtocol}>
                  <option value="rtsp_h264">RTSP H.264</option>
                  <option value="rtsp_mjpeg">RTSP MJPEG</option>
                  <option value="http_jpeg">HTTP JPEG</option>
                </select>
                {#if validationErrors['protocol']}
                  <p class="th-color-danger text-xs mt-1">{validationErrors['protocol']}</p>
                {/if}
              </div>

              <!-- URL -->
              <div class="md:col-span-2">
                <label for="cam-url" class="input-label">{t('cameras.url')}</label>
                <input id="cam-url" type="text" class="input" bind:value={formUrl} />
                {#if validationErrors['url']}
                  <p class="th-color-danger text-xs mt-1">{validationErrors['url']}</p>
                {/if}
              </div>

              <!-- Username -->
              <div>
                <label for="cam-user" class="input-label">{t('cameras.username')}</label>
                <input id="cam-user" type="text" class="input" bind:value={formUsername} />
              </div>

              <!-- Password -->
              <div>
                <label for="cam-pass" class="input-label">{t('cameras.password')}</label>
                <input id="cam-pass" type="password" class="input" bind:value={formPassword} />
              </div>

              <!-- Enabled -->
              <div class="md:col-span-2 flex items-center gap-2">
                <input id="cam-enabled" type="checkbox" class="accent-[var(--color-accent)]" bind:checked={formEnabled} />
                <label for="cam-enabled" class="th-text-secondary text-sm">{t('cameras.enabledToggle')}</label>
              </div>
            </div>

            <div class="flex items-center gap-3 mt-6">
              <button on:click={handleSubmit} class="btn btn-primary" disabled={saving}>
                {#if saving}
                  <span class="spinner mr-2"></span>
                {/if}
                {t('cameras.save')}
              </button>
              <button on:click={resetForm} class="btn btn-ghost">
                {t('cameras.cancel')}
              </button>
            </div>
          </div>
        {/if}

        <!-- Delete Confirmation -->
        {#if deletingCamera}
          <div class="card p-6 border th-border-danger bg-[rgba(239,68,68,0.2)]">
            <h3 class="text-lg font-semibold th-color-danger mb-2">{t('cameras.deleteTitle')}</h3>
            <p class="th-text-secondary mb-4">
              {t('cameras.deleteMessage', { name: deletingCamera.name })}
            </p>
            <div class="flex items-center gap-3">
              <button on:click={handleDelete} class="px-4 py-2 th-bg-danger hover:th-bg-danger-light text-white rounded-md transition-colors">
                {t('cameras.deleteConfirm')}
              </button>
              <button on:click={() => deletingCamera = null} class="btn btn-ghost">
                {t('cameras.cancel')}
              </button>
            </div>
          </div>
        {/if}

        <!-- Camera Table -->
        <div class="card border th-border overflow-hidden">
          {#if cameras.length === 0}
            <div class="p-8 text-center th-text-muted">
              {t('cameras.noCameras')}
            </div>
          {:else}
            <div class="overflow-x-auto">
              <table class="min-w-full divide-y divide-[var(--border)]">
                <thead>
                  <tr>
                    <th class="px-6 py-3 text-left text-xs font-medium th-text-muted uppercase tracking-wider">{t('cameras.tableName')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium th-text-muted uppercase tracking-wider">{t('cameras.tableProtocol')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium th-text-muted uppercase tracking-wider">{t('cameras.tableStatus')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium th-text-muted uppercase tracking-wider">{t('cameras.tableUrl')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium th-text-muted uppercase tracking-wider">{t('cameras.tableActions')}</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-[var(--border)]">
                  {#each cameras as camera (camera.id)}
                    <tr class="hover:th-bg-hover transition-colors">
                      <td class="px-6 py-4 whitespace-nowrap text-sm font-medium th-text-primary">{camera.name}</td>
                      <td class="px-6 py-4 whitespace-nowrap text-sm th-text-secondary">{camera.protocol}</td>
                      <td class="px-6 py-4 whitespace-nowrap text-sm">
                        {#if camera.enabled}
                          <span class="badge badge-success">{t('cameras.enabled')}</span>
                        {:else}
                          <span class="badge badge-error">{t('cameras.disabled')}</span>
                        {/if}
                      </td>
                      <td class="px-6 py-4 text-sm th-text-secondary max-w-xs truncate">{camera.url}</td>
                      <td class="px-6 py-4 whitespace-nowrap text-sm">
                        <div class="flex gap-2">
                          <button
                            on:click={() => openEditForm(camera)}
                            class="btn btn-ghost px-2 py-1 text-sm transition-all duration-200"
                          >{t('cameras.edit')}</button>
                          <button
                            on:click={() => deletingCamera = camera}
                            class="btn btn-ghost px-2 py-1 text-sm th-color-danger transition-all duration-200"
                          >{t('cameras.delete')}</button>
                        </div>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {/if}
        </div>

      </div>
    {/if}
  </main>
</div>
