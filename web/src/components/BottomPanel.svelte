<script lang="ts">
  import {
    Search,
    Users,
    Activity,
    TerminalSquare,
    Gauge,
    ChevronDown,
    ChevronUp,
    Play,
    Pause,
    ArrowDownLeft,
    ArrowUpRight,
    AlertTriangle,
  } from 'lucide-svelte';
  import TerminalPanel from './TerminalPanel.svelte';
  import {
    bytes,
    duration,
    type Lab,
    type Runtime,
    type Event,
    type Device,
  } from '../lib/types';
  let {
    lab,
    runtime,
    events,
    tab = $bindable('subscribers'),
    selectedId,
    onselect,
    onaction,
    onsetup,
    history,
  }: {
    lab: Lab;
    runtime: Runtime | null;
    events: Event[];
    tab: string;
    selectedId: string | null;
    onselect: (id: string) => void;
    onaction: (kind: string, id: string) => void;
    onsetup: () => void;
    history: number[];
  } = $props();
  let collapsed = $state(false);
  let query = $state('');
  let filter = $state('all');
  const sessions = $derived(
    new Map(runtime?.sessions.map((s) => [s.onuId, s]) || [])
  );
  const filtered = $derived(
    lab.subscribers.filter(
      (s) =>
        (!query ||
          `${s.username} ${s.onuId} ${s.address}`
            .toLowerCase()
            .includes(query.toLowerCase())) &&
        (filter === 'all' ||
          (sessions.get(s.onuId)?.status || 'stopped') === filter)
    )
  );
  const terminalNode = $derived(lab.nodes.find((n) => n.id === selectedId));
  const m = $derived(runtime?.metrics);
  const points = $derived(
    history
      .map(
        (n, i) =>
          `${(i / Math.max(1, history.length - 1)) * 600},${68 - (n / Math.max(1, lab.subscribers.length)) * 60}`
      )
      .join(' ')
  );
</script>

<section class="bottom-panel" class:collapsed>
  <div class="bottom-toolbar">
    <div class="bottom-tabs">
      <button
        class:active={tab === 'subscribers'}
        onclick={() => {
          tab = 'subscribers';
          collapsed = false;
        }}
        ><Users size={14} />Subscribers<span>{lab.subscribers.length}</span
        ></button
      ><button
        class:active={tab === 'activity'}
        onclick={() => {
          tab = 'activity';
          collapsed = false;
        }}
        ><Activity size={14} />Activity{#if lab.faults.length}<span
            class="warning-count">{lab.faults.length}</span
          >{/if}</button
      ><button
        class:active={tab === 'terminal'}
        onclick={() => {
          tab = 'terminal';
          collapsed = false;
        }}><TerminalSquare size={14} />Terminal</button
      ><button
        class:active={tab === 'performance'}
        onclick={() => {
          tab = 'performance';
          collapsed = false;
        }}><Gauge size={14} />Performance</button
      >
    </div>
    <div class="bottom-toolbar-right">
      {#if tab === 'subscribers' && !collapsed}<div class="compact-search">
          <Search size={13} /><input
            aria-label="Filter subscribers"
            bind:value={query}
            placeholder="Filter subscribers…"
          />
        </div>
        <select
          aria-label="Filter session state"
          class="compact-select"
          bind:value={filter}
          ><option value="all">All states</option><option value="connected"
            >Connected</option
          ><option value="stopped">Stopped</option><option value="offline"
            >Offline</option
          ><option value="suspended">Suspended</option><option
            value="connecting">Connecting</option
          ></select
        >{:else if tab === 'terminal'}<span class="small-text mono"
          >{terminalNode?.label || 'No device selected'}</span
        >{/if}<button
        class="icon-button small"
        onclick={() => (collapsed = !collapsed)}
        title={collapsed ? 'Expand panel' : 'Collapse panel'}
        >{#if collapsed}<ChevronUp size={16} />{:else}<ChevronDown
            size={16}
          />{/if}</button
      >
    </div>
  </div>
  {#if !collapsed}<div
      class="bottom-content"
      class:terminal-content={tab === 'terminal'}
    >
      {#if tab === 'subscribers'}<div class="table-scroll">
          <table class="subscriber-table">
            <thead
              ><tr
                ><th>SUBSCRIBER</th><th>ONU</th><th>SESSION</th><th
                  >IP ADDRESS</th
                ><th>PROFILE</th><th><ArrowDownLeft size={11} /> RX</th><th
                  ><ArrowUpRight size={11} /> TX</th
                ><th>UPTIME</th><th></th></tr
              ></thead
            ><tbody
              >{#each filtered as subscriber (subscriber.id)}{@const session =
                  sessions.get(subscriber.onuId)}<tr
                  class:selected={selectedId === subscriber.onuId}
                  onclick={() => onselect(subscriber.onuId)}
                  ><td
                    ><span class="subscriber-avatar"
                      >{subscriber.username.slice(-2)}</span
                    ><strong>{subscriber.username}</strong
                    >{#if !subscriber.enabled}<Pause
                        size={11}
                        class="muted"
                      />{/if}</td
                  ><td class="mono muted">{subscriber.onuId}</td><td
                    ><span
                      class="table-status status-{session?.status || 'stopped'}"
                      ><i></i>{session?.status || 'stopped'}</span
                    ></td
                  ><td class="mono">{session?.address || '—'}</td><td
                    class="mono muted">{subscriber.rateLimit}</td
                  ><td class="mono muted">{bytes(session?.rxBytes)}</td><td
                    class="mono muted">{bytes(session?.txBytes)}</td
                  ><td class="mono muted"
                    >{session?.status === 'connected'
                      ? duration(session.uptimeSeconds)
                      : '—'}</td
                  ><td
                    >{#if lab.radius.mode === 'builtin'}<button
                        class="row-action"
                        title={subscriber.enabled
                          ? 'Suspend subscriber'
                          : 'Restore subscriber'}
                        onclick={(e) => {
                          e.stopPropagation();
                          onaction(
                            subscriber.enabled ? 'suspend' : 'resume',
                            subscriber.onuId
                          );
                        }}
                        >{#if subscriber.enabled}<Pause size={13} />{:else}<Play
                            size={13}
                          />{/if}</button
                      >{/if}</td
                  ></tr
                >{/each}</tbody
            >
          </table>
          {#if !filtered.length}<div class="small-empty">
              {lab.subscribers.length
                ? 'No matching subscribers.'
                : 'Add an ONU to create its PPPoE subscriber.'}
            </div>{/if}
        </div>
      {:else if tab === 'activity'}<div class="activity-list">
          {#each [...events].reverse() as event (event.seq)}<div
              class="activity-row level-{event.level}"
            >
              <span class="event-dot"></span><time
                >{new Date(event.at).toLocaleTimeString([], {
                  hour12: false,
                })}</time
              ><span class="event-kind">{event.kind}</span><strong
                >{event.subject}</strong
              ><span class="event-message">{event.message}</span>
            </div>{/each}{#if !events.length}<div class="small-empty">
              Device, RADIUS, and runtime events appear here as they happen.
            </div>{/if}
        </div>
      {:else if tab === 'terminal'}{#key `${terminalNode?.id}-${runtime?.phase}`}<TerminalPanel
            {lab}
            node={terminalNode}
            {runtime}
            {onsetup}
          />{/key}
      {:else if tab === 'performance'}<div class="performance-panel">
          <div class="session-chart">
            <div>
              <span class="eyebrow">OBSERVED PPPoE SESSIONS</span><strong
                >{m?.activeSessions || 0}<small>
                  / {lab.subscribers.length}</small
                ></strong
              >
            </div>
            <svg
              viewBox="0 0 600 80"
              preserveAspectRatio="none"
              aria-label="Observed active sessions over time"
              ><line x1="0" y1="68" x2="600" y2="68" stroke="#dbe7e0" /><line
                x1="0"
                y1="20"
                x2="600"
                y2="20"
                stroke="#edf2ef"
                stroke-dasharray="4 4"
              /><polyline
                {points}
                fill="none"
                stroke="#218d65"
                stroke-width="2"
                vector-effect="non-scaling-stroke"
              /></svg
            ><span class="field-hint"
              >Actual kernel interfaces. Each point comes from runtime
              telemetry.</span
            >
          </div>
          <div class="memory-breakdown">
            <span class="eyebrow">MEMORY · PROCESS RSS</span>
            <dl>
              <div>
                <dt>Go application</dt>
                <dd>{bytes(m?.goRssBytes)}</dd>
              </div>
              <div>
                <dt>Network helper</dt>
                <dd>{bytes(m?.workerRssBytes)}</dd>
              </div>
              <div>
                <dt>RouterOS VMs</dt>
                <dd>{bytes(m?.qemuRssBytes)}</dd>
              </div>
              <div>
                <dt>PPP clients</dt>
                <dd>{bytes(m?.pppRssBytes)}</dd>
              </div>
            </dl>
            <p class="field-hint">
              Measured separately. Shared pages and kernel memory require
              separate accounting in load tests.
            </p>
          </div>
        </div>{/if}
    </div>{/if}
</section>
