<script lang="ts">
  import {
    X,
    Cable as CableIcon,
    Copy,
    TerminalSquare,
    Power,
    RotateCcw,
    Unplug,
    PlugZap,
    ShieldCheck,
    Trash2,
    Radio,
    Settings2,
    ChevronRight,
    Eye,
    EyeOff,
    AlertCircle,
  } from 'lucide-svelte';
  import DeviceGlyph from './DeviceGlyph.svelte';
  import {
    kindNames,
    bytes,
    type Lab,
    type Device,
    type Cable,
    type Runtime,
    type NodeConfig,
  } from '../lib/types';
  import { copy } from '../lib/api';
  let {
    lab,
    runtime,
    node,
    link,
    onclose,
    onnode,
    onlink,
    onaction,
    onfault,
    onrepair,
    onremove,
    onterminal,
    oncapture,
    onrename,
    onsetup,
    onintegrate,
  }: {
    lab: Lab;
    runtime: Runtime | null;
    node?: Device;
    link?: Cable;
    onclose: () => void;
    onnode: (id: string, config: Partial<NodeConfig>, label?: string) => void;
    onlink: (id: string, patch: Partial<Cable>) => void;
    onaction: (kind: string, target: string, value?: string) => void;
    onfault: (kind: string, type: string, id: string, value?: number) => void;
    onrepair: (id: string) => void;
    onremove: (type: 'node' | 'link', id: string) => void;
    onterminal: (id: string) => void;
    oncapture: (id: string) => void;
    onrename: (name: string) => void;
    onsetup: () => void;
    onintegrate: () => void;
  } = $props();
  let tab = $state<'details' | 'config'>('details');
  let reveal = $state(false);
  let attenuation = $state(12);
  const nodeState = $derived(node ? runtime?.nodes[node.id] : undefined);
  const subscriber = $derived(
    node ? lab.subscribers.find((s) => s.onuId === node.id) : undefined
  );
  const session = $derived(
    node ? runtime?.sessions.find((s) => s.onuId === node.id) : undefined
  );
  const running = $derived(runtime?.phase === 'running');
  const acsState = $derived(node ? runtime?.acs?.[node.id] : undefined);
  const acsEnabled = $derived(
    !!lab.acs?.enabled && !node?.config.acs?.disabled
  );
  const locked = $derived(
    !!runtime && !['stopped', 'error'].includes(runtime.phase)
  );
  const targetFaults = $derived(
    lab.faults.filter((f) => f.targetId === (node?.id || link?.id))
  );
  const incoming = $derived(
    node ? lab.links.find((e) => e.target === node.id) : undefined
  );
  const parent = $derived(
    incoming ? lab.nodes.find((n) => n.id === incoming.source) : undefined
  );
  function number(event: Event): number {
    return Number((event.target as HTMLInputElement).value);
  }
</script>

<aside class="inspector">
  <div class="panel-heading">
    <span
      >{node
        ? 'DEVICE INSPECTOR'
        : link
          ? 'CABLE INSPECTOR'
          : 'LAB OVERVIEW'}</span
    >{#if node || link}<button
        class="icon-button small"
        onclick={onclose}
        title="Close inspector"><X size={15} /></button
      >{:else}<Settings2 size={14} />{/if}
  </div>
  {#if node}
    <div class="inspector-identity">
      <span class="large-symbol kind-{node.kind}"
        ><DeviceGlyph kind={node.kind} size={28} /></span
      >
      <div>
        <span class="eyebrow">{kindNames[node.kind]}</span>
        <h2>{node.label}</h2>
        <span class="small-text mono">{node.id}</span>
      </div>
    </div>
    <div class="inspector-status">
      <span class="status-pill status-{nodeState?.status || 'ready'}"
        ><i></i>{nodeState?.status || 'ready'}</span
      ><span class="small-text"
        >{node.kind === 'onu'
          ? (session?.status || 'stopped') + ' PPPoE'
          : node.config.model}</span
      >
    </div>
    <div class="segmented">
      <button class:active={tab === 'details'} onclick={() => (tab = 'details')}
        >Overview</button
      ><button class:active={tab === 'config'} onclick={() => (tab = 'config')}
        >Configure</button
      >
    </div>
    <div class="inspector-scroll">
      {#if tab === 'details'}
        {#if nodeState?.reason}<div class="context-note">
            <span class="status-dot status-{nodeState.status}"
            ></span>{nodeState.reason}
          </div>{/if}
        {#if node.kind === 'onu'}
          <section class="inspector-section">
            <h3>OPTICAL PATH <span class="tiny-tag">MODELED</span></h3>
            <div class="optical-reading">
              <Radio size={20} /><strong
                >{nodeState?.rxDbm !== undefined
                  ? nodeState.rxDbm.toFixed(2)
                  : '—'}</strong
              ><span>dBm</span>
            </div>
            <div class="signal-track">
              <span
                style={`width: ${nodeState?.rxDbm !== undefined ? Math.max(2, Math.min(100, ((nodeState.rxDbm + 35) / 35) * 100)) : 0}%`}
              ></span><i style="left:23%"></i>
            </div>
            <div class="scale-labels">
              <span>−35 dBm</span><span>0 dBm</span>
            </div>
            <dl>
              <div>
                <dt>Serial number</dt>
                <dd class="mono">{node.config.serial}</dd>
              </div>
              {#if node.config.mac}<div>
                  <dt>MAC / EPON identity</dt>
                  <dd class="mono">
                    {node.config.mac.replaceAll(':', '').toUpperCase()}
                  </dd>
                </div>{/if}
              <div>
                <dt>OLT / PON</dt>
                <dd>
                  {nodeState?.oltId || 'Unconnected'} / {nodeState?.pon?.toUpperCase() ||
                    '—'}
                </dd>
              </div>
              <div>
                <dt>Sensitivity</dt>
                <dd>{node.config.sensitivityDbm} dBm</dd>
              </div>
              <div>
                <dt>Registration</dt>
                <dd>{node.config.registered ? 'Authorized' : 'Pending'}</dd>
              </div>
            </dl>
          </section>
          <section class="inspector-section">
            <h3>SUBSCRIBER SERVICE</h3>
            <dl>
              <div>
                <dt>Account</dt>
                <dd class="mono">{subscriber?.username || 'None'}</dd>
              </div>
              <div>
                <dt>VLAN</dt>
                <dd>{node.config.vlan}</dd>
              </div>
              <div>
                <dt>IPv4 address</dt>
                <dd class="mono">{session?.address || 'Not connected'}</dd>
              </div>
              <div>
                <dt>RADIUS policy</dt>
                <dd>
                  {lab.radius.mode === 'external'
                    ? 'External server'
                    : subscriber?.enabled
                      ? 'Active'
                      : 'Suspended'}
                </dd>
              </div>
              <div>
                <dt>Profile</dt>
                <dd>{subscriber?.rateLimit || '—'}</dd>
              </div>
              <div>
                <dt>Received / sent</dt>
                <dd>{bytes(session?.rxBytes)} / {bytes(session?.txBytes)}</dd>
              </div>
            </dl>
            {#if subscriber && lab.radius.mode === 'builtin'}<button
                class="button full"
                class:danger-outline={subscriber.enabled}
                onclick={() =>
                  onaction(subscriber.enabled ? 'suspend' : 'resume', node!.id)}
                ><ShieldCheck size={14} />{subscriber.enabled
                  ? 'Suspend subscriber'
                  : 'Restore subscriber'}</button
              >{/if}
          </section>
          <section class="inspector-section">
            <h3>ACS · TR-069</h3>
            <dl>
              <div>
                <dt>Status</dt>
                <dd>
                  {acsEnabled
                    ? acsState?.status || 'Waiting for runtime'
                    : 'Disabled'}
                </dd>
              </div>
              <div>
                <dt>Inform count</dt>
                <dd>{acsState?.informCount || 0}</dd>
              </div>
              <div>
                <dt>Last Inform</dt>
                <dd>
                  {acsState?.lastInform
                    ? new Date(acsState.lastInform).toLocaleTimeString()
                    : '—'}
                </dd>
              </div>
            </dl>
            {#if acsState?.deviceId}<p class="field-hint mono acs-identity">
                {acsState.deviceId}
              </p>{/if}
            {#if acsState?.lastError}<p class="field-hint error-text">
                {acsState.lastError}
              </p>{/if}
            <button
              class="button full"
              disabled={!acsEnabled || session?.status !== 'connected'}
              onclick={() => onaction('acs_inform', node!.id)}
              ><Radio size={14} />Send Inform now</button
            >
            <button class="button full mt-8" onclick={onsetup}
              ><Settings2 size={14} />ACS service settings</button
            >
          </section>
        {:else if node.kind === 'router' || node.kind === 'olt'}
          <section class="inspector-section">
            <h3>MANAGEMENT ENDPOINT</h3>
            <div class="endpoint">
              <code>{nodeState?.managementIp || 'Assigned at runtime'}</code
              ><button
                class="icon-button small"
                title="Copy management IP"
                onclick={() => copy(nodeState?.managementIp || '')}
                ><Copy size={14} /></button
              >
            </div>
            <dl>
              <div>
                <dt>SSH</dt>
                <dd class="mono">TCP 22</dd>
              </div>
              <div>
                <dt>SNMP v2c</dt>
                <dd class="mono">UDP 161</dd>
              </div>
              <div>
                <dt>{node.kind === 'router' ? 'RouterOS API' : 'Telnet'}</dt>
                <dd class="mono">
                  TCP {node.kind === 'router' ? '8728' : '23'}
                </dd>
              </div>
              <div>
                <dt>Username</dt>
                <dd class="mono">{node.config.username}</dd>
              </div>
            </dl>
            <button
              class="button full"
              disabled={!running}
              onclick={() => onterminal(node!.id)}
              ><TerminalSquare size={14} />Open SSH console<ChevronRight
                size={14}
              /></button
            >
          </section>
          {#if node.kind === 'olt'}<div class="reference-note">
              <ShieldCheck size={17} />
              <div>
                <strong>Explicit compatibility</strong>
                <p>
                  HSGQ GPON and EPON SNMP report ONU identity, status and
                  optical power. Choose HSGQ in your FTTH app and use the MAC
                  identity from Integration. The CLI uses <code>lab</code> commands.
                </p>
              </div>
            </div>{/if}
        {:else}<section class="inspector-section">
            <h3>PASSIVE INFRASTRUCTURE</h3>
            <dl>
              <div>
                <dt>Type</dt>
                <dd>{node.config.model}</dd>
              </div>
              {#if node.kind === 'splitter' || node.kind === 'odp'}<div>
                  <dt>Split ratio</dt>
                  <dd>1:{node.config.splitRatio}</dd>
                </div>
                <div>
                  <dt>Insertion loss</dt>
                  <dd>
                    {(
                      10 * Math.log10(node.config.splitRatio || 8) +
                      0.5 * Math.log2(node.config.splitRatio || 8)
                    ).toFixed(2)} dB
                  </dd>
                </div>{/if}
              <div>
                <dt>Upstream</dt>
                <dd>{parent?.label || 'Not connected'}</dd>
              </div>
            </dl>
          </section>{/if}
        <section class="inspector-section">
          <h3>DEVICE CONTROLS</h3>
          <div class="action-grid">
            <button
              class="button"
              onclick={() =>
                onaction(
                  node!.config.powered ? 'power_off' : 'power_on',
                  node!.id
                )}
              ><Power size={14} />{node.config.powered
                ? 'Power off'
                : 'Power on'}</button
            ><button
              class="button"
              onclick={() =>
                onaction(node!.config.adminUp ? 'disable' : 'enable', node!.id)}
              ><Unplug size={14} />{node.config.adminUp
                ? 'Disable'
                : 'Enable'}</button
            >{#if node.kind === 'onu'}<button
                class="button"
                onclick={() => onaction('reboot', node!.id)}
                ><RotateCcw size={14} />Reboot</button
              ><button
                class="button"
                onclick={() =>
                  onaction(
                    node!.config.registered ? 'deauthorize' : 'authorize',
                    node!.id
                  )}
                ><ShieldCheck size={14} />{node.config.registered
                  ? 'Deauthorize'
                  : 'Authorize'}</button
              >{/if}
          </div>
          {#if node.kind === 'olt'}
            <div class="pon-controls">
              {#each Array.from({ length: 8 }, (_, i) => `pon${i + 1}`) as port}
                {@const active = lab.faults.find(
                  (f) =>
                    f.kind === 'pon_down' &&
                    f.targetId === `${node!.id}:${port}`
                )}
                <button
                  class="button"
                  class:danger={!!active}
                  title={active ? `Restore ${port}` : `Disable ${port}`}
                  onclick={() =>
                    active
                      ? onrepair(active.id)
                      : onfault('pon_down', 'port', `${node!.id}:${port}`)}
                >
                  <Radio size={12} />{port.toUpperCase()}{active ? ' ↓' : ''}
                </button>
              {/each}
            </div>
          {/if}
          {#if node.kind === 'onu'}<button
              class="button full mt-8"
              disabled={!running}
              onclick={() => onterminal(node!.id)}
              ><TerminalSquare size={14} />Open subscriber terminal</button
            >{/if}
        </section>
      {:else}
        <section class="inspector-section form-stack">
          <h3>DEVICE SETTINGS</h3>
          <label
            >Display name<input
              value={node.label}
              onchange={(e) => onnode(node!.id, {}, e.currentTarget.value)}
            /></label
          >{#if node.kind === 'onu'}<label
              >GPON serial<input
                class="mono"
                disabled={locked}
                value={node.config.serial || ''}
                onchange={(e) =>
                  onnode(node!.id, { serial: e.currentTarget.value })}
              /></label
            ><label
              >Service VLAN<input
                type="number"
                min="1"
                max="4094"
                value={node.config.vlan}
                onchange={(e) => onnode(node!.id, { vlan: number(e) })}
              /></label
            ><label
              >Receiver sensitivity · dBm<input
                type="number"
                step="0.5"
                min="-40"
                max="0"
                value={node.config.sensitivityDbm}
                onchange={(e) =>
                  onnode(node!.id, { sensitivityDbm: number(e) })}
              /></label
            >{/if}
          {#if node.kind === 'splitter' || node.kind === 'odp'}<label
              >Split ratio<select
                disabled={locked}
                value={node.config.splitRatio}
                onchange={(e) =>
                  onnode(node!.id, {
                    splitRatio: Number(e.currentTarget.value),
                  })}
                >{#each [2, 4, 8, 16, 32, 64, 128] as ratio}<option
                    value={ratio}>1:{ratio}</option
                  >{/each}</select
              ></label
            >{/if}
          {#if node.kind === 'olt'}<label
              >Optical transmit power · dBm<input
                type="number"
                step="0.5"
                min="-10"
                max="15"
                value={node.config.txDbm}
                onchange={(e) => onnode(node!.id, { txDbm: number(e) })}
              /></label
            >{/if}
          {#if node.kind === 'router'}<label
              >Memory · MiB<select
                disabled={locked}
                value={node.config.memoryMb}
                onchange={(e) =>
                  onnode(node!.id, { memoryMb: Number(e.currentTarget.value) })}
                >{#each [512, 1024, 2048, 4096] as memory}<option value={memory}
                    >{memory} MiB</option
                  >{/each}</select
              ></label
            ><label
              >PPPoE service VLANs<input
                disabled={locked}
                value={node.config.serviceVlans?.join(', ')}
                onchange={(e) =>
                  onnode(node!.id, {
                    serviceVlans: e.currentTarget.value.split(',').map(Number),
                  })}
              /><span class="field-hint">Comma separated, e.g. 100, 200</span
              ></label
            >{/if}
        </section>
        {#if node.kind === 'onu'}
          <section class="inspector-section form-stack">
            <h3>ACS OVERRIDE</h3>
            <label
              >ACS participation<select
                aria-label="ACS participation"
                value={node.config.acs?.disabled ? 'disabled' : 'enabled'}
                disabled={locked}
                onchange={(e) =>
                  onnode(node!.id, {
                    acs: {
                      ...node!.config.acs,
                      disabled: e.currentTarget.value === 'disabled',
                    },
                  })}
                ><option value="enabled">Use lab ACS</option><option
                  value="disabled">Disable for this ONU</option
                ></select
              ></label
            >
            <label
              >ONU ACS URL<input
                class="mono"
                type="url"
                disabled={locked}
                value={node.config.acs?.url || ''}
                placeholder="Inherit lab ACS"
                onchange={(e) =>
                  onnode(node!.id, {
                    acs: {
                      disabled: false,
                      ...node!.config.acs,
                      url: e.currentTarget.value,
                    },
                  })}
              /></label
            >
            {#if node.config.acs?.url}
              <label
                >ONU ACS username<input
                  disabled={locked}
                  value={node.config.acs.username || ''}
                  onchange={(e) =>
                    onnode(node!.id, {
                      acs: {
                        disabled: false,
                        ...node!.config.acs,
                        username: e.currentTarget.value,
                      },
                    })}
                /></label
              >
              <label
                >ONU ACS password<input
                  type="password"
                  disabled={locked}
                  value={node.config.acs.password || ''}
                  onchange={(e) =>
                    onnode(node!.id, {
                      acs: {
                        disabled: false,
                        ...node!.config.acs,
                        password: e.currentTarget.value,
                      },
                    })}
                /></label
              >
            {/if}
            <label
              >ONU Inform interval · seconds<input
                type="number"
                min="0"
                max="86400"
                disabled={locked}
                value={node.config.acs?.periodicInformSeconds || 0}
                onchange={(e) =>
                  onnode(node!.id, {
                    acs: {
                      disabled: false,
                      ...node!.config.acs,
                      periodicInformSeconds: number(e),
                    },
                  })}
              /><span class="field-hint">0 inherits the lab interval.</span
              ></label
            >
          </section>
        {/if}
        {#if node.kind === 'router' || node.kind === 'olt'}<section
            class="inspector-section form-stack"
          >
            <h3>LAB CREDENTIALS</h3>
            <label
              >Username<input
                disabled={locked || node.kind === 'router'}
                value={node.config.username || ''}
                onchange={(e) =>
                  onnode(node!.id, { username: e.currentTarget.value })}
              /></label
            ><label
              >Password
              <div class="input-with-action">
                <input
                  type={reveal ? 'text' : 'password'}
                  disabled={locked}
                  value={node.config.password || ''}
                  onchange={(e) =>
                    onnode(node!.id, { password: e.currentTarget.value })}
                /><button
                  class="icon-button small"
                  title="Toggle password visibility"
                  onclick={() => (reveal = !reveal)}
                  >{#if reveal}<EyeOff size={14} />{:else}<Eye
                      size={14}
                    />{/if}</button
                >
              </div></label
            ><label
              >SNMP community<input
                disabled={locked}
                value={node.config.community || ''}
                onchange={(e) =>
                  onnode(node!.id, { community: e.currentTarget.value })}
              /></label
            >
          </section>{/if}
        <section class="inspector-section">
          <button
            class="button full danger-outline"
            disabled={locked}
            onclick={() => onremove('node', node!.id)}
            ><Trash2 size={14} />Remove device</button
          >{#if locked}<p class="field-hint">
              Stop the runtime to change topology or credentials.
            </p>{/if}
        </section>
      {/if}
      {#if targetFaults.length}<section class="inspector-section">
          <h3>ACTIVE FAULTS</h3>
          {#each targetFaults as fault}<button
              class="fault-row"
              onclick={() => onrepair(fault.id)}
              ><AlertCircle size={14} />{fault.kind.replaceAll('_', ' ')}<span
                >Repair</span
              ></button
            >{/each}
        </section>{/if}
    </div>
  {:else if link}
    <div class="inspector-identity">
      <span class="large-symbol kind-odf"><CableIcon size={26} /></span>
      <div>
        <span class="eyebrow"
          >{link.medium === 'fiber' ? 'OPTICAL FIBER' : 'ETHERNET'}</span
        >
        <h2>
          {link.id.startsWith('feeder')
            ? 'Feeder cable'
            : link.id.startsWith('drop')
              ? 'Drop cable'
              : 'Network cable'}
        </h2>
        <span class="small-text mono">{link.id}</span>
      </div>
    </div>
    <div class="inspector-scroll">
      <section class="inspector-section">
        <h3>CONNECTED PORTS</h3>
        <div class="path-endpoint">
          <i></i>
          <div>
            <strong
              >{lab.nodes.find((n) => n.id === link!.source)?.label}</strong
            ><span class="mono">{link.sourcePort}</span>
          </div>
        </div>
        <div class="path-line"></div>
        <div class="path-endpoint">
          <i></i>
          <div>
            <strong
              >{lab.nodes.find((n) => n.id === link!.target)?.label}</strong
            ><span class="mono">{link.targetPort}</span>
          </div>
        </div>
      </section>
      <section class="inspector-section form-stack">
        <h3>CABLE PROPERTIES</h3>
        <label
          >Length · meters<input
            type="number"
            min="0"
            max="20000"
            value={link.lengthM}
            disabled={locked}
            onchange={(e) => onlink(link!.id, { lengthM: number(e) })}
          /></label
        ><label
          >Connector / splice loss · dB<input
            type="number"
            min="0"
            max="60"
            step="0.1"
            value={link.lossDb}
            disabled={locked}
            onchange={(e) => onlink(link!.id, { lossDb: number(e) })}
          /></label
        >{#if link.medium === 'fiber'}<div class="metric-line">
            <span>Calculated path loss</span><strong
              >{((link.lengthM / 1000) * 0.25 + link.lossDb).toFixed(2)} dB</strong
            >
          </div>{/if}
      </section>
      <section class="inspector-section">
        <h3>FAULT INJECTION</h3>
        {#if targetFaults.some((f) => f.kind === 'cable_cut')}<button
            class="button full success-outline"
            onclick={() =>
              onrepair(targetFaults.find((f) => f.kind === 'cable_cut')!.id)}
            ><PlugZap size={15} />Repair cable</button
          >{:else}<button
            class="button full danger-outline"
            onclick={() => onfault('cable_cut', 'link', link!.id)}
            ><Unplug size={15} />Cut this cable</button
          >{/if}
        <p class="field-hint">
          Affects this cable's downstream path. Live clients lose their actual
          connection when the runtime is active.
        </p>
        {#if link.medium === 'fiber'}<div class="attenuation-control">
            <label
              >Additional attenuation <span>{attenuation} dB</span><input
                type="range"
                min="1"
                max="40"
                step="1"
                bind:value={attenuation}
              /></label
            ><button
              class="button full"
              disabled={targetFaults.some((f) => f.kind === 'attenuation')}
              onclick={() =>
                onfault('attenuation', 'link', link!.id, attenuation)}
              >Apply attenuation</button
            >
          </div>{/if}
        {#each targetFaults.filter((f) => f.kind !== 'cable_cut') as fault}<button
            class="fault-row"
            onclick={() => onrepair(fault.id)}
            ><AlertCircle size={14} />{fault.kind} · {fault.value} dB<span
              >Repair</span
            ></button
          >{/each}
      </section>
      <section class="inspector-section">
        <button
          class="button full"
          disabled={!running}
          onclick={() => oncapture(link!.id)}
          ><Radio size={14} />Capture packets · 10s</button
        ><button
          class="button full danger-text mt-8"
          disabled={locked}
          onclick={() => onremove('link', link!.id)}
          ><Trash2 size={14} />Remove cable</button
        >
      </section>
    </div>
  {:else}
    <div class="inspector-scroll">
      <div class="overview-title">
        <span class="eyebrow">YOUR NETWORK SANDBOX</span>
        <h2>Every connection.<br />Every consequence.</h2>
        <p>
          Build a network. Introduce a fault.<br />See what your application
          sees.
        </p>
      </div>
      <section class="inspector-section form-stack">
        <label
          >Lab name<input
            value={lab.name}
            onchange={(e) => onrename(e.currentTarget.value)}
          /></label
        >
        <div class="overview-counts">
          <div><strong>{lab.nodes.length}</strong><span>Devices</span></div>
          <div><strong>{lab.links.length}</strong><span>Cables</span></div>
          <div>
            <strong>{lab.subscribers.length}</strong><span>Subscribers</span>
          </div>
        </div>
      </section>
      <section class="inspector-section">
        <h3>SERVICE DESIGN</h3>
        <dl>
          <div>
            <dt>Access network</dt>
            <dd>GPON · HSGQ SNMP compatibility</dd>
          </div>
          <div>
            <dt>Subscriber sessions</dt>
            <dd>PPPoE / IPv4</dd>
          </div>
          <div>
            <dt>Authentication</dt>
            <dd>
              {lab.radius.mode === 'builtin'
                ? 'Built-in RADIUS'
                : 'External RADIUS'}
            </dd>
          </div>
          <div>
            <dt>Runtime</dt>
            <dd>{runtime?.phase || 'stopped'}</dd>
          </div>
        </dl>
        <button class="button full" onclick={onsetup}
          ><Settings2 size={14} />Runtime & RADIUS settings</button
        ><button class="button full mt-8" onclick={onintegrate}
          ><PlugZap size={14} />Connect your application</button
        >
      </section>
      <div class="quick-guide">
        <h3>A SMALL GUIDE TO BIG NETWORKS</h3>
        <div>
          <span>01</span>
          <p>
            <strong>Place your devices</strong>Drag from the library onto the
            canvas.
          </p>
        </div>
        <div>
          <span>02</span>
          <p>
            <strong>Connect the ports</strong>Fiber to fiber. Ethernet to
            Ethernet.
          </p>
        </div>
        <div>
          <span>03</span>
          <p>
            <strong>Bring it to life</strong>Run the lab, then select a cable to
            inject a fault.
          </p>
        </div>
      </div>
      <div class="canvas-key">
        <span><i class="key-fiber"></i>Optical fiber</span><span
          ><i class="key-ethernet"></i>Ethernet</span
        ><span><i class="key-fault"></i>Fault active</span>
      </div>
    </div>
  {/if}
</aside>
