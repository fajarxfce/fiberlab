<script lang="ts">
  import {
    ArrowDown,
    ArrowUp,
    Play,
    Square,
    LoaderCircle,
  } from 'lucide-svelte';
  import { api } from '../lib/api';
  import {
    bitrate,
    bytes,
    duration,
    type Lab,
    type Runtime,
    type TrafficConfig,
  } from '../lib/types';

  let {
    lab,
    runtime,
    onruntime,
    onselect,
  }: {
    lab: Lab;
    runtime: Runtime | null;
    onruntime: (view: Runtime) => void;
    onselect: (id: string) => void;
  } = $props();
  let target = $state('all');
  let down = $state<number | undefined>(64);
  let up = $state<number | undefined>(16);
  let seconds = $state(300);
  let busy = $state(false);
  let error = $state('');
  let loadedRun = $state('');
  const traffic = $derived(runtime?.traffic);
  const active = $derived(traffic?.state === 'running');
  const ready = $derived(runtime?.phase === 'running');
  const sessions = $derived(
    new Map(runtime?.sessions.map((s) => [s.onuId, s]) || [])
  );
  const streams = $derived(
    new Map(traffic?.onus.map((s) => [s.onuId, s]) || [])
  );
  const targetIDs = $derived(
    target === 'all'
      ? lab.subscribers.map((s) => s.onuId)
      : target === 'current'
        ? traffic?.config.onuIds || []
        : [target]
  );
  const selectedIDs = $derived(active ? traffic!.config.onuIds : targetIDs);
  const rows = $derived(
    lab.subscribers.filter((s) => selectedIDs.includes(s.onuId))
  );
  const download = $derived(
    ready
      ? rows.reduce(
          (n, s) => n + (sessions.get(s.onuId)?.rxBitsPerSecond || 0),
          0
        )
      : 0
  );
  const upload = $derived(
    ready
      ? rows.reduce(
          (n, s) => n + (sessions.get(s.onuId)?.txBitsPerSecond || 0),
          0
        )
      : 0
  );
  const elapsed = $derived(
    traffic
      ? Math.max(
          0,
          Math.floor(
            (new Date(traffic.stoppedAt || new Date()).getTime() -
              new Date(traffic.startedAt).getTime()) /
              1000
          )
        )
      : 0
  );
  const validation = $derived(
    !targetIDs.length
      ? 'Add an ONU subscriber first.'
      : [down, up].some(
            (n) =>
              typeof n !== 'number' ||
              !Number.isInteger(n) ||
              n < 0 ||
              n > 100_000
          )
        ? 'Enter whole-number rates between 0 and 100,000 Kbps.'
        : !down && !up
          ? 'Set a positive download or upload rate.'
          : ((down || 0) + (up || 0)) * targetIDs.length > 100_000
            ? 'The combined target for all ONUs must be 100 Mbps or less.'
            : ''
  );

  $effect(() => {
    if (traffic && traffic.startedAt !== loadedRun) {
      loadedRun = traffic.startedAt;
      down = traffic.config.downloadKbps;
      up = traffic.config.uploadKbps;
      seconds = traffic.config.durationSeconds;
      target =
        traffic.config.onuIds.length === 1
          ? traffic.config.onuIds[0]
          : traffic.config.onuIds.length === lab.subscribers.length
            ? 'all'
            : 'current';
    }
  });

  async function change(stop: boolean) {
    if (busy || (!stop && validation)) return;
    busy = true;
    error = '';
    try {
      const config: TrafficConfig = {
        onuIds: targetIDs,
        downloadKbps: down || 0,
        uploadKbps: up || 0,
        durationSeconds: seconds,
      };
      const next = await api<Runtime>(
        `/labs/${lab.id}/traffic`,
        stop ? 'DELETE' : 'POST',
        stop ? undefined : config
      );
      onruntime(next);
    } catch (reason) {
      error = reason instanceof Error ? reason.message : String(reason);
    } finally {
      busy = false;
    }
  }
</script>

<div class="traffic-panel">
  <form
    class="traffic-controls"
    onsubmit={(event) => {
      event.preventDefault();
      void change(false);
    }}
  >
    <div class="traffic-title">
      <strong>Subscriber traffic</strong><span class="tiny-tag"
        >REAL PACKETS</span
      >
    </div>
    <div class="traffic-fields">
      <label
        >Target ONUs<select
          aria-label="Traffic target"
          bind:value={target}
          disabled={active || busy}
        >
          <option value="all">All {lab.subscribers.length} ONUs</option>
          {#if target === 'current'}<option value="current"
              >Current selection ({targetIDs.length} ONUs)</option
            >{/if}
          {#each lab.subscribers as sub (sub.id)}<option value={sub.onuId}
              >{sub.username} · {sub.onuId}</option
            >{/each}
        </select></label
      >
      <label
        >Duration<select
          aria-label="Traffic duration"
          bind:value={seconds}
          disabled={active || busy}
        >
          <option value={60}>1 minute</option><option value={300}
            >5 minutes</option
          ><option value={900}>15 minutes</option><option value={3600}
            >1 hour</option
          ><option value={0}>Until stopped</option>
          {#if ![0, 60, 300, 900, 3600].includes(seconds)}<option
              value={seconds}>{seconds} seconds</option
            >{/if}
        </select></label
      >
      <label
        >Download / ONU <span>Kbps</span><input
          aria-label="Download per ONU (Kbps)"
          type="number"
          min="0"
          max="100000"
          step="1"
          bind:value={down}
          disabled={active || busy}
          required
        /></label
      >
      <label
        >Upload / ONU <span>Kbps</span><input
          aria-label="Upload per ONU (Kbps)"
          type="number"
          min="0"
          max="100000"
          step="1"
          bind:value={up}
          disabled={active || busy}
          required
        /></label
      >
    </div>
    <div class="traffic-actions">
      {#if active}<button
          class="button danger-outline"
          type="button"
          disabled={busy || !ready}
          onclick={() => change(true)}
        >
          {#if busy}<LoaderCircle size={13} class="spin" />{:else}<Square
              size={12}
            />{/if}Stop traffic
        </button>
      {:else}<button
          class="button primary"
          type="submit"
          disabled={busy || !ready || !!validation}
        >
          {#if busy}<LoaderCircle size={13} class="spin" />{:else}<Play
              size={13}
            />{/if}Start traffic
        </button>{/if}
      <span class="small-text traffic-state" aria-live="polite"
        >{!ready
          ? 'Start the lab first'
          : active
            ? `Running · ${duration(elapsed)}`
            : traffic?.state === 'completed'
              ? 'Completed'
              : traffic
                ? 'Stopped'
                : 'Ready'}</span
      >
    </div>
    {#if error || validation}<p class="traffic-error" role="alert">
        {error || validation}
      </p>{/if}
    <p class="field-hint">
      Targets are per ONU; 0 disables a direction. CHR free limits each
      interface to 1 Mbps, shared by these sessions.
    </p>
  </form>
  <div class="traffic-observations">
    <div class="traffic-summary">
      <div>
        <span><ArrowDown size={13} />OBSERVED DOWNLOAD</span><strong
          data-testid="traffic-download">{bitrate(download)}</strong
        >
      </div>
      <div>
        <span><ArrowUp size={13} />OBSERVED UPLOAD</span><strong
          data-testid="traffic-upload">{bitrate(upload)}</strong
        >
      </div>
      <p>
        ONU → PPPoE → MikroTik → WAN lab<br /><span
          >Uses local traffic, with no Internet quota.</span
        >
      </p>
    </div>
    <div class="table-scroll traffic-table-wrap">
      <table class="subscriber-table traffic-table">
        <thead
          ><tr
            ><th>SUBSCRIBER</th><th>PATH</th><th>DOWNLOAD</th><th>UPLOAD</th><th
              >SESSION BYTES ↓ / ↑</th
            ></tr
          ></thead
        >
        <tbody
          >{#each rows as sub (sub.id)}{@const session = sessions.get(
              sub.onuId
            )}{@const stream = streams.get(sub.onuId)}
            <tr>
              <td
                ><button
                  class="traffic-subscriber"
                  onclick={() => onselect(sub.onuId)}>{sub.username}</button
                ><span class="traffic-onu-id">{sub.onuId}</span></td
              >
              <td title={active ? stream?.error : undefined}
                ><span
                  class="table-status status-{session?.status || 'stopped'}"
                  ><i></i>{active && stream?.state === 'waiting'
                    ? 'Waiting for path'
                    : session?.status || 'stopped'}</span
                ></td
              >
              <td class="mono"
                >{bitrate(ready ? session?.rxBitsPerSecond : 0)}</td
              ><td class="mono"
                >{bitrate(ready ? session?.txBitsPerSecond : 0)}</td
              >
              <td class="mono muted"
                >{bytes(session?.rxBytes)} / {bytes(session?.txBytes)}</td
              >
            </tr>
          {/each}</tbody
        >
      </table>
      {#if !rows.length}<div class="small-empty">
          Add an ONU to generate subscriber traffic.
        </div>{/if}
    </div>
    <p class="field-hint traffic-counters-note">
      Measured from PPP interfaces every 3 seconds. MikroTik PPP RX = upload, TX
      = download. RADIUS graphs update at the accounting interval.
    </p>
  </div>
</div>
