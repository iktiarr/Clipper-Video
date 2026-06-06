<script>
  import { Upload } from 'tus-js-client';

  // ── Config ──────────────────────────────────────────────────────────────────
  const API_URL = import.meta.env.VITE_API_URL || 'http://localhost:8081';

  // ── State ───────────────────────────────────────────────────────────────────
  let selectedFile  = $state(null);
  let fileError     = $state('');
  let duration      = $state('5');      // '1' | '2' | '5' | '10' | 'custom'
  let customMins    = $state(1);

  // idle | uploading | processing | completed | failed
  let status        = $state('idle');
  let statusMsg     = $state('');
  let uploadPct     = $state(0);
  let jobId         = $state('');
  let clips         = $state([]);
  let pollTimer     = $state(null);
  let currentUpload = $state(null);   // tus Upload instance
  let isPaused      = $state(false);

  const MAX_SIZE = 5 * 1024 * 1024 * 1024; // 5 GB

  const durations = [
    { label: '1 menit',  value: '1'  },
    { label: '2 menit',  value: '2'  },
    { label: '5 menit',  value: '5'  },
    { label: '10 menit', value: '10' },
    { label: 'Custom',   value: 'custom' },
  ];

  function effectiveDuration() {
    return duration === 'custom' ? Number(customMins) : Number(duration);
  }

  function formatSize(bytes) {
    if (bytes >= 1_073_741_824) return (bytes / 1_073_741_824).toFixed(2) + ' GB';
    if (bytes >= 1_048_576)     return (bytes / 1_048_576).toFixed(1)    + ' MB';
    return (bytes / 1024).toFixed(0) + ' KB';
  }

  // ── File selection ───────────────────────────────────────────────────────────
  function handleFileChange(e) {
    const file = e.target?.files?.[0] ?? e;
    if (!file) return;
    if (file.size > MAX_SIZE) {
      fileError    = 'Ukuran file melebihi batas 5 GB.';
      selectedFile = null;
      return;
    }
    if (!file.type.startsWith('video/')) {
      fileError    = 'Hanya file video yang diizinkan.';
      selectedFile = null;
      return;
    }
    fileError    = '';
    selectedFile = file;
  }

  function handleDrop(e) {
    e.preventDefault();
    const file = e.dataTransfer?.files?.[0];
    if (file) handleFileChange(file);
  }

  function openFilePicker() {
    document.getElementById('file-input').click();
  }

  // ── TUS Upload ───────────────────────────────────────────────────────────────
  function startUpload() {
    if (!selectedFile) return;
    const mins = effectiveDuration();
    if (!mins || mins < 1 || mins > 60) {
      alert('Durasi harus antara 1 sampai 60 menit.');
      return;
    }

    status        = 'uploading';
    statusMsg     = 'Mempersiapkan upload...';
    uploadPct     = 0;
    clips         = [];
    isPaused      = false;

    const upload = new Upload(selectedFile, {
      // TUS endpoint
      endpoint: `${API_URL}/files/`,

      // Resume support: try 5 times with increasing delays
      retryDelays: [0, 3000, 5000, 10000, 20000],

      // 50 MB per chunk – good balance for large files
      chunkSize: 50 * 1024 * 1024,

      // Metadata passed to server (not used for processing, just informational)
      metadata: {
        filename: selectedFile.name,
        filetype: selectedFile.type || 'video/mp4',
      },

      onError(error) {
        console.error('[TUS] Upload error:', error);
        status        = 'failed';
        statusMsg     = 'Upload gagal: ' + (error?.message ?? String(error));
        currentUpload = null;
      },

      onProgress(bytesUploaded, bytesTotal) {
        uploadPct = Math.round((bytesUploaded / bytesTotal) * 100);
        statusMsg = `Mengupload... ${uploadPct}%  (${formatSize(bytesUploaded)} / ${formatSize(bytesTotal)})`;
      },

      async onSuccess() {
        // Extract upload ID from the TUS Location URL
        // URL is like: http://localhost:8081/files/{uploadId}
        const uploadId = upload.url.split('/').pop();
        console.log('[TUS] Upload complete, uploadId:', uploadId);

        status    = 'processing';
        statusMsg = 'Upload selesai – memulai pemotongan video...';
        currentUpload = null;

        // Trigger FFmpeg processing on the backend
        try {
          const res = await fetch(`${API_URL}/api/start`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ uploadId, duration: mins }),
          });

          if (!res.ok) {
            const err = await res.json().catch(() => ({}));
            throw new Error(err.error ?? `Server error ${res.status}`);
          }

          const data = await res.json();
          jobId = data.jobId;
          console.log('[API] Job created:', jobId);
          startPolling(jobId);
        } catch (e) {
          status    = 'failed';
          statusMsg = 'Gagal memulai proses: ' + e.message;
        }
      },
    });

    currentUpload = upload;
    upload.start();
  }

  // ── Pause / Resume ───────────────────────────────────────────────────────────
  function pauseUpload() {
    if (currentUpload && !isPaused) {
      currentUpload.abort();
      isPaused  = true;
      statusMsg = `Upload dijeda di ${uploadPct}% – klik Lanjutkan untuk melanjutkan`;
    }
  }

  function resumeUpload() {
    if (currentUpload && isPaused) {
      currentUpload.start();
      isPaused  = false;
      statusMsg = 'Melanjutkan upload...';
    }
  }

  // ── Polling ──────────────────────────────────────────────────────────────────
  function startPolling(id) {
    stopPolling();
    pollTimer = setInterval(() => pollJob(id), 2000);
  }

  function stopPolling() {
    if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
  }

  async function pollJob(id) {
    try {
      const res = await fetch(`${API_URL}/api/jobs/${id}`);
      if (!res.ok) {
        stopPolling();
        status    = 'failed';
        statusMsg = 'Gagal mengambil status job.';
        return;
      }
      const data = await res.json();

      if (data.status === 'completed') {
        stopPolling();
        status    = 'completed';
        statusMsg = data.message;
        clips     = data.clips ?? [];
      } else if (data.status === 'failed') {
        stopPolling();
        status    = 'failed';
        statusMsg = data.message ?? 'Proses gagal.';
      } else {
        statusMsg = data.message ?? 'Memproses...';
      }
    } catch {
      stopPolling();
      status    = 'failed';
      statusMsg = 'Gagal menghubungi server.';
    }
  }

  // ── Delete / Reset ───────────────────────────────────────────────────────────
  async function deleteJob() {
    stopPolling();
    if (currentUpload) { currentUpload.abort(); currentUpload = null; }
    if (jobId) {
      try { await fetch(`${API_URL}/api/jobs/${jobId}`, { method: 'DELETE' }); } catch {}
    }
    reset();
  }

  function reset() {
    stopPolling();
    if (currentUpload) { currentUpload.abort(); currentUpload = null; }
    selectedFile  = null;
    fileError     = '';
    status        = 'idle';
    statusMsg     = '';
    uploadPct     = 0;
    jobId         = '';
    clips         = [];
    isPaused      = false;
    const inp = document.getElementById('file-input');
    if (inp) inp.value = '';
  }

  function downloadUrl(clip) {
    return `${API_URL}${clip.downloadUrl}`;
  }

  const statusIcon = {
    uploading:  '⬆️',
    processing: '⚙️',
    completed:  '✅',
    failed:     '❌',
  };
</script>

<div class="container">

  <!-- ── Header ───────────────────────────────────────────────────────── -->
  <div class="header">
    <h1>🎬 Video Clipper Machine</h1>
    <p>Upload video, pilih durasi potongan, lalu download hasil clip.</p>
  </div>

  <!-- ── File picker ──────────────────────────────────────────────────── -->
  <div class="card">
    <h2>1. Pilih File Video</h2>

    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="file-zone"
      onclick={openFilePicker}
      ondragover={(e) => e.preventDefault()}
      ondrop={handleDrop}
    >
      <div class="icon">📁</div>
      <div>Klik untuk pilih video, atau drag &amp; drop ke sini</div>
      <div class="hint">Maksimal 5 GB &nbsp;|&nbsp; MP4, MKV, WebM, MOV, AVI, dll.</div>
      <input id="file-input" type="file" accept="video/*" onchange={handleFileChange} />
    </div>

    {#if fileError}
      <div class="file-error">⚠️ {fileError}</div>
    {/if}

    {#if selectedFile}
      <div class="file-info">
        <span class="name">📄 {selectedFile.name}</span>
        <span class="size">{formatSize(selectedFile.size)}</span>
      </div>
    {/if}
  </div>

  <!-- ── Duration ─────────────────────────────────────────────────────── -->
  <div class="card">
    <h2>2. Pilih Durasi Potongan</h2>

    <div class="duration-options">
      {#each durations as d}
        <label class:active={duration === d.value}>
          <input type="radio" bind:group={duration} value={d.value} />
          {d.label}
        </label>
      {/each}
    </div>

    {#if duration === 'custom'}
      <div class="custom-input">
        <input
          type="number"
          bind:value={customMins}
          min="1"
          max="60"
          placeholder="Menit"
        />
        <span>menit (1–60)</span>
      </div>
    {/if}
  </div>

  <!-- ── Action ───────────────────────────────────────────────────────── -->
  <div class="card">
    <h2>3. Mulai Proses</h2>

    <button
      class="btn btn-primary"
      style="width: 100%"
      onclick={startUpload}
      disabled={!selectedFile || status === 'uploading' || status === 'processing'}
    >
      {#if status === 'uploading' || status === 'processing'}
        <span class="spinner"></span> Sedang Memproses...
      {:else}
        ⚡ Upload dan Potong Video
      {/if}
    </button>

    <!-- Status box -->
    {#if status !== 'idle'}
      <div style="margin-top: 14px;">
        <div class="status-box {status}">
          <div class="status-label {status}">
            {#if status === 'uploading' || status === 'processing'}
              <span class="spinner"></span>
            {:else}
              {statusIcon[status]}
            {/if}
            {status === 'uploading'  ? 'Mengupload'  :
             status === 'processing' ? 'Memproses'   :
             status === 'completed'  ? 'Selesai'      : 'Gagal'}
          </div>
          <div class="status-msg">{statusMsg}</div>

          {#if status === 'uploading'}
            <div class="progress-bar-wrap">
              <div class="progress-bar-fill" style="width: {uploadPct}%"></div>
            </div>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Pause / Resume / Cancel during upload -->
    {#if status === 'uploading'}
      <div class="btn-row">
        {#if !isPaused}
          <button class="btn btn-secondary btn-sm" onclick={pauseUpload}>⏸ Pause Upload</button>
        {:else}
          <button class="btn btn-primary  btn-sm" onclick={resumeUpload}>▶ Lanjutkan Upload</button>
        {/if}
        <button class="btn btn-danger btn-sm" onclick={reset}>✕ Batal</button>
      </div>
    {:else if status !== 'idle'}
      <div class="btn-row">
        <button class="btn btn-secondary" onclick={reset}>🔄 Reset</button>
        {#if status === 'completed' && clips.length > 0}
          <button class="btn btn-danger btn-sm" onclick={deleteJob}>🗑️ Hapus Hasil</button>
        {/if}
      </div>
    {/if}
  </div>

  <!-- ── Results ──────────────────────────────────────────────────────── -->
  {#if status === 'completed' && clips.length > 0}
    <div class="card">
      <h2>Hasil Potongan Video</h2>
      <div class="clips-count">Total: {clips.length} clip &nbsp;|&nbsp; Mode: stream copy (kualitas original 100%)</div>
      <div class="clips-list">
        {#each clips as clip}
          <div class="clip-item">
            <div class="clip-info">
              <span class="clip-name">🎥 {clip.name}</span>
              <span class="clip-size">{formatSize(clip.size)}</span>
            </div>
            <a
              href={downloadUrl(clip)}
              download={clip.name}
              class="btn btn-primary btn-sm"
            >
              ⬇️ Download
            </a>
          </div>
        {/each}
      </div>

      <div style="margin-top: 12px; padding: 10px; background: var(--gray-50); border-radius: 6px; font-size: 12px; color: var(--gray-500);">
        ⚠️ File akan otomatis dihapus dari server setelah 2 jam. Segera download sebelum batas waktu.
      </div>
    </div>
  {/if}

  {#if status === 'completed' && clips.length === 0}
    <div class="card">
      <div style="color: var(--warning); text-align: center; padding: 12px;">
        ⚠️ Proses selesai namun tidak ada clip yang dihasilkan. Video mungkin terlalu pendek untuk durasi yang dipilih.
      </div>
    </div>
  {/if}

</div>
