<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { listCameras, createCamera, updateCamera, deleteCamera, logout } from '$lib/api';
  import type { Camera, CreateCameraRequest, UpdateCameraRequest } from '$lib/api';
  import LanguageSwitcher from '../components/LanguageSwitcher.svelte';
  import { t, onLangChange, getCurrentLang } from '$lib/i18n';

  let lang = getCurrentLang();
  const unsubscribe = onLangChange(() => { lang = getCurrentLang(); });
  onDestroy(() => { unsubscribe(); });

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
    feedback = msg;
    feedbackType = type;
    setTimeout(() => { feedback = ''; }, 3000);
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

<div class="min-h-screen bg-slate-900">
  <!-- Header -->
  <header class="bg-slate-800 border-b border-slate-700">
    <div class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
      <div class="flex items-center justify-between h-16">
        <div class="flex items-center gap-4">
          <h1 class="text-xl font-bold text-slate-100">MiBee NVR</h1>
          <nav class="flex gap-4">
            <a href="#/recordings" class="text-slate-300 hover:text-slate-100 transition-colors">
              {t('nav.recordings')}
            </a>
            <a href="#/cameras" class="text-cyan-500 font-medium">{t('nav.cameras')}</a>
            <a href="#/stats" class="text-slate-300 hover:text-slate-100 transition-colors">{t('nav.stats')}</a>
            <a href="#/settings" class="text-slate-300 hover:text-slate-100 transition-colors">{t('nav.settings')}</a>
          </nav>
        </div>
        <div class="flex items-center gap-3">
          <LanguageSwitcher />
          <button on:click={logout} class="btn btn-ghost">
            {t('nav.logout')}
          </button>
        </div>
      </div>
    </div>
  </header>

  <!-- Main content -->
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="flex items-center justify-between mb-6">
      <h2 class="text-2xl font-bold text-slate-100">{t('cameras.title')}</h2>
      <button on:click={openAddForm} class="btn btn-primary">
        + {t('cameras.addCamera')}
      </button>
    </div>

    <!-- Feedback -->
    {#if feedback}
      <div class="mb-4 p-3 rounded-md border {feedbackType === 'success'
        ? 'bg-emerald-900/30 border-emerald-700 text-emerald-400'
        : 'bg-red-900/30 border-red-700 text-red-400'}">
        {feedback}
      </div>
    {/if}

    <!-- Error -->
    {#if error}
      <div class="mb-4 p-4 bg-red-900/30 border border-red-700 rounded-md text-red-300">
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
          <div class="card p-6 border border-slate-700/60">
            <h3 class="text-lg font-semibold text-slate-100 mb-4">
              {editingCamera ? t('cameras.editCamera') : t('cameras.addCamera')}
            </h3>

            <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
              <!-- Name -->
              <div>
                <label for="cam-name" class="input-label">{t('cameras.name')}</label>
                <input id="cam-name" type="text" class="input" bind:value={formName} />
                {#if validationErrors['name']}
                  <p class="text-red-400 text-xs mt-1">{validationErrors['name']}</p>
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
                  <p class="text-red-400 text-xs mt-1">{validationErrors['protocol']}</p>
                {/if}
              </div>

              <!-- URL -->
              <div class="md:col-span-2">
                <label for="cam-url" class="input-label">{t('cameras.url')}</label>
                <input id="cam-url" type="text" class="input" bind:value={formUrl} />
                {#if validationErrors['url']}
                  <p class="text-red-400 text-xs mt-1">{validationErrors['url']}</p>
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
                <input id="cam-enabled" type="checkbox" class="accent-cyan-500" bind:checked={formEnabled} />
                <label for="cam-enabled" class="text-slate-300 text-sm">{t('cameras.enabledToggle')}</label>
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
          <div class="card p-6 border border-red-700/60 bg-red-900/20">
            <h3 class="text-lg font-semibold text-red-300 mb-2">{t('cameras.deleteTitle')}</h3>
            <p class="text-slate-300 mb-4">
              {t('cameras.deleteMessage', { name: deletingCamera.name })}
            </p>
            <div class="flex items-center gap-3">
              <button on:click={handleDelete} class="px-4 py-2 bg-red-600 hover:bg-red-500 text-white rounded-md transition-colors">
                {t('cameras.deleteConfirm')}
              </button>
              <button on:click={() => deletingCamera = null} class="btn btn-ghost">
                {t('cameras.cancel')}
              </button>
            </div>
          </div>
        {/if}

        <!-- Camera Table -->
        <div class="card border border-slate-700/60 overflow-hidden">
          {#if cameras.length === 0}
            <div class="p-8 text-center text-slate-400">
              {t('cameras.noCameras')}
            </div>
          {:else}
            <div class="overflow-x-auto">
              <table class="min-w-full divide-y divide-slate-700">
                <thead>
                  <tr>
                    <th class="px-6 py-3 text-left text-xs font-medium text-slate-400 uppercase tracking-wider">{t('cameras.tableName')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-slate-400 uppercase tracking-wider">{t('cameras.tableProtocol')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-slate-400 uppercase tracking-wider">{t('cameras.tableStatus')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-slate-400 uppercase tracking-wider">{t('cameras.tableUrl')}</th>
                    <th class="px-6 py-3 text-left text-xs font-medium text-slate-400 uppercase tracking-wider">{t('cameras.tableActions')}</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-slate-700">
                  {#each cameras as camera (camera.id)}
                    <tr class="hover:bg-slate-800/50 transition-colors">
                      <td class="px-6 py-4 whitespace-nowrap text-sm font-medium text-slate-100">{camera.name}</td>
                      <td class="px-6 py-4 whitespace-nowrap text-sm text-slate-300">{camera.protocol}</td>
                      <td class="px-6 py-4 whitespace-nowrap text-sm">
                        {#if camera.enabled}
                          <span class="text-emerald-400">{t('cameras.enabled')}</span>
                        {:else}
                          <span class="text-slate-500">{t('cameras.disabled')}</span>
                        {/if}
                      </td>
                      <td class="px-6 py-4 text-sm text-slate-300 max-w-xs truncate">{camera.url}</td>
                      <td class="px-6 py-4 whitespace-nowrap text-sm">
                        <div class="flex gap-2">
                          <button
                            on:click={() => openEditForm(camera)}
                            class="text-slate-400 hover:text-cyan-400 transition-colors"
                          >{t('cameras.edit')}</button>
                          <button
                            on:click={() => deletingCamera = camera}
                            class="text-slate-400 hover:text-red-400 transition-colors"
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
