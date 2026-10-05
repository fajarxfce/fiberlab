<script lang="ts">
  import {
    Router,
    Network,
    Server,
    Split,
    Cable,
    Box,
    Radio,
  } from 'lucide-svelte';
  import type { Kind } from '../lib/types';
  let { kind, size = 22 }: { kind: Kind; size?: number } = $props();
  const icons = {
    router: Router,
    switch: Network,
    olt: Server,
    odf: Cable,
    splitter: Split,
    odp: Box,
    onu: Radio,
  };
  const Icon = $derived(icons[kind] || Box);
</script>

<Icon {size} strokeWidth={1.7} />
