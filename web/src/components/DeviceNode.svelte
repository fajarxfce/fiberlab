<script lang="ts">
  import { Handle, Position, type NodeProps, type Node } from '@xyflow/svelte';
  import DeviceGlyph from './DeviceGlyph.svelte';
  import { ports, kindNames, type DeviceData } from '../lib/types';
  let { data, selected }: NodeProps<Node<DeviceData>> = $props();
  const device = $derived(data.device);
  const inputs = $derived(ports(device).filter((p) => p.direction === 'in'));
  const outputs = $derived(ports(device).filter((p) => p.direction === 'out'));
  const state = $derived(data.state?.status || 'ready');
  const active = $derived(data.session?.status === 'connected');
  const bad = $derived(
    ['los', 'offline', 'unregistered', 'unknown'].includes(state)
  );
</script>

<div
  class="device-node kind-{device.kind}"
  class:selected
  class:bad
  data-device-id={device.id}
>
  {#each inputs as p}
    <Handle
      id={p.id}
      type="target"
      position={Position.Top}
      class="port-handle medium-{p.medium}"
    />
    <span class="input-caption">{p.label}</span>
  {/each}
  <div class="device-node-main">
    <span class="device-symbol"
      ><DeviceGlyph
        kind={device.kind}
        size={device.kind === 'olt' ? 25 : 23}
      /></span
    >
    <div class="device-node-copy">
      <span class="device-category">{kindNames[device.kind]}</span><strong
        title={device.label}>{device.label}</strong
      >
    </div>
    <span
      class="device-status-dot status-{state}"
      title={data.state?.reason || 'Runtime stopped'}
    ></span>
  </div>
  <div class="device-node-foot">
    {#if device.kind === 'onu'}<span class="mono"
        >VLAN {device.config.vlan}</span
      ><span class:connected={active}
        >{active
          ? 'PPPoE online'
          : data.state?.rxDbm !== undefined
            ? `${data.state.rxDbm.toFixed(1)} dBm`
            : state === 'ready'
              ? 'Ready'
              : state.toUpperCase()}</span
      >
    {:else if device.kind === 'olt'}<span>8 PON ports</span><span class="mono"
        >REFERENCE</span
      >
    {:else if device.kind === 'router'}<span>RouterOS CHR</span><span
        class="mono">{device.config.memoryMb} MiB</span
      >
    {:else if device.kind === 'splitter' || device.kind === 'odp'}<span
        >Passive optical</span
      ><span class="mono">1:{device.config.splitRatio}</span>
    {:else}<span
        >{device.kind === 'switch'
          ? '802.1Q bridge'
          : 'Fiber distribution'}</span
      ><span class="mono"
        >{state === 'ready' ? 'READY' : state.toUpperCase()}</span
      >{/if}
  </div>
  {#each outputs as p, index}
    <Handle
      id={p.id}
      type="source"
      position={Position.Bottom}
      class="port-handle medium-{p.medium}"
      style={`left: ${8 + ((index + 0.5) * 84) / outputs.length}%;`}
    />
  {/each}
</div>
