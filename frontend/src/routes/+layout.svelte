<script>
  import '../app.css';
  let { children } = $props();
</script>

<svelte:head>
  <title>Video Clipper Machine</title>
  <meta name="description" content="Upload video, pilih durasi potongan, lalu download hasil clip." />
</svelte:head>

{@render children()}
