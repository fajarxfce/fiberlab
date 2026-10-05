export type Kind =
  'router' | 'switch' | 'olt' | 'odf' | 'splitter' | 'odp' | 'onu';
export type NodeConfig = {
  powered: boolean;
  adminUp: boolean;
  registered: boolean;
  serial?: string;
  mac?: string;
  vlan: number;
  serviceVlans?: number[];
  splitRatio?: number;
  txDbm: number;
  sensitivityDbm: number;
  model?: string;
  username?: string;
  password?: string;
  community?: string;
  memoryMb?: number;
  acs?: ONUACS;
};
export type Device = {
  id: string;
  kind: Kind;
  label: string;
  position: { x: number; y: number };
  config: NodeConfig;
};
export type Cable = {
  id: string;
  source: string;
  sourcePort: string;
  target: string;
  targetPort: string;
  medium: 'fiber' | 'ethernet';
  lengthM: number;
  lossDb: number;
};
export type Subscriber = {
  id: string;
  onuId: string;
  username: string;
  password: string;
  enabled: boolean;
  rateLimit: string;
  address: string;
};
export type Radius = {
  mode: 'builtin' | 'external';
  address: string;
  authPort: number;
  accountingPort: number;
  secret: string;
  interimSeconds: number;
};
export type ACS = {
  enabled: boolean;
  url: string;
  username: string;
  password: string;
  periodicInformSeconds: number;
  connectionRequestListen: string;
  connectionRequestUrl: string;
  connectionRequestUsername: string;
  connectionRequestPassword: string;
};
export type ONUACS = {
  disabled: boolean;
  url?: string;
  username?: string;
  password?: string;
  periodicInformSeconds?: number;
};
export type ACSStatus = {
  status: 'waiting' | 'informing' | 'online' | 'offline' | 'error';
  deviceId?: string;
  connectionRequestUrl?: string;
  lastInform?: string;
  nextInform?: string;
  informCount: number;
  lastError?: string;
};
export function defaultACS(): ACS {
  return {
    enabled: false,
    url: 'http://127.0.0.1:7547',
    username: '',
    password: '',
    periodicInformSeconds: 60,
    connectionRequestListen: '127.0.0.1:7548',
    connectionRequestUrl: 'http://127.0.0.1:7548',
    connectionRequestUsername: 'fiberlab',
    connectionRequestPassword: secret(),
  };
}
export type Fault = {
  id: string;
  kind: string;
  targetType: string;
  targetId: string;
  value: number;
  createdAt: string;
};
export type Lab = {
  version: number;
  id: string;
  name: string;
  description: string;
  revision: number;
  createdAt: string;
  updatedAt: string;
  nodes: Device[];
  links: Cable[];
  subscribers: Subscriber[];
  radius: Radius;
  faults: Fault[];
  imageId: string;
  trapAddress?: string;
  acs?: ACS;
};
export type NodeState = {
  id: string;
  status: string;
  reason: string;
  opticalUp: boolean;
  servicePathUp: boolean;
  rxDbm?: number;
  oltId?: string;
  pon?: string;
  path?: string[];
  managementIp?: string;
};
export type Session = {
  onuId: string;
  username: string;
  status: string;
  address?: string;
  peer?: string;
  rxBytes: number;
  txBytes: number;
  uptimeSeconds: number;
  sessionId?: string;
  lastAccounting?: string;
  updatedAt: string;
};
export type Event = {
  seq: number;
  labId: string;
  at: string;
  kind: string;
  level: string;
  subject: string;
  message: string;
};
export type Metrics = {
  activeSessions: number;
  configuredSessions: number;
  rxBytes: number;
  txBytes: number;
  goRssBytes: number;
  workerRssBytes: number;
  qemuRssBytes: number;
  pppRssBytes: number;
};
export type Runtime = {
  labId: string;
  runId: string;
  phase: string;
  message: string;
  startedAt?: string;
  nodes: Record<string, NodeState>;
  sessions: Session[];
  metrics: Metrics;
  progress: number;
  acs?: Record<string, ACSStatus>;
};
export type Check = {
  name: string;
  ok: boolean;
  required: boolean;
  detail: string;
};
export type System = {
  helperOnline: boolean;
  helperLaunch?: { available: boolean; starting: boolean; error?: string };
  checks: Check[];
  helperCommand: string;
  dataDir: string;
  socket: string;
  network: { management: string; testOrigin: string; subscribers: string };
};
export type Image = {
  id: string;
  version: string;
  sha256: string;
  size: number;
  path: string;
  source: string;
  addedAt: string;
};
export type ImageStatus = {
  images: Image[];
  job: {
    status: string;
    version: string;
    received: number;
    total: number;
    error?: string;
  };
  defaultVersion: string;
};
export type Port = {
  id: string;
  label: string;
  medium: string;
  direction: 'in' | 'out';
};
export type DeviceData = {
  device: Device;
  state?: NodeState;
  session?: Session;
  [key: string]: unknown;
};

export const kindNames: Record<Kind, string> = {
  router: 'MikroTik',
  switch: 'Switch',
  olt: 'HSGQ OLT',
  odf: 'ODF',
  splitter: 'Splitter',
  odp: 'ODP',
  onu: 'ONU',
};
export const descriptions: Record<Kind, string> = {
  router: 'RouterOS CHR',
  switch: 'Ethernet · 8 ports',
  olt: 'GPON · 8 ports',
  odf: 'Fiber patch panel',
  splitter: 'Passive · 1:8',
  odp: 'Distribution · 1:8',
  onu: 'GPON + PPPoE client',
};
export function ports(d: Device): Port[] {
  const p: Port[] = [];
  const add = (
    id: string,
    label: string,
    medium: string,
    direction: 'in' | 'out'
  ) => p.push({ id, label, medium, direction });
  if (d.kind === 'router')
    for (let i = 1; i <= 4; i++)
      add(`lan${i}`, `ether${i + 2}`, 'ethernet', 'out');
  if (d.kind === 'switch') {
    add('uplink', 'UPLINK', 'ethernet', 'in');
    for (let i = 1; i <= 8; i++) add(`port${i}`, `GE ${i}`, 'ethernet', 'out');
  }
  if (d.kind === 'olt') {
    add('uplink', 'UPLINK', 'ethernet', 'in');
    for (let i = 1; i <= 8; i++) add(`pon${i}`, `PON ${i}`, 'fiber', 'out');
  }
  if (d.kind === 'odf') {
    add('in', 'IN', 'fiber', 'in');
    add('out1', 'OUT', 'fiber', 'out');
  }
  if (d.kind === 'odp' || d.kind === 'splitter') {
    add('in', 'IN', 'fiber', 'in');
    for (let i = 1; i <= (d.config.splitRatio || 8); i++)
      add(`out${i}`, `${i}`, 'fiber', 'out');
  }
  if (d.kind === 'onu') add('pon', 'PON', 'fiber', 'in');
  return p;
}
export function bytes(n = 0): string {
  if (n < 1024) return `${n} B`;
  const i = Math.min(3, Math.floor(Math.log(n) / Math.log(1024)));
  return `${(n / 1024 ** i).toFixed(1)} ${['B', 'KiB', 'MiB', 'GiB'][i]}`;
}
export function duration(n = 0): string {
  return n < 60
    ? `${n}s`
    : n < 3600
      ? `${Math.floor(n / 60)}m ${n % 60}s`
      : `${Math.floor(n / 3600)}h ${Math.floor((n % 3600) / 60)}m`;
}
export function secret(): string {
  return [...crypto.getRandomValues(new Uint8Array(12))]
    .map((v) => v.toString(16).padStart(2, '0'))
    .join('');
}
export function makeDevice(
  kind: Kind,
  count: number,
  position: { x: number; y: number }
): Device {
  const config: NodeConfig = {
    powered: true,
    adminUp: true,
    registered: true,
    vlan: 100,
    serviceVlans: [100],
    splitRatio: 8,
    txDbm: 4,
    sensitivityDbm: -27,
    community: 'lab-read',
    memoryMb: 1024,
    model: descriptions[kind],
  };
  if (kind === 'router' || kind === 'olt') {
    config.username = 'admin';
    config.password = secret();
  }
  if (kind === 'olt') config.model = 'HSGQ-G08R · reference';
  if (kind === 'onu') {
    config.serial = `HSGQ${secret().slice(0, 8).toUpperCase()}`;
    config.txDbm = 2.5;
    config.mac = `02:46:54:${secret().slice(0, 6).match(/../g)!.join(':')}`;
  }
  return {
    id: `${kind}-${secret().slice(0, 8)}`,
    kind,
    label: `${kindNames[kind]} ${String(count).padStart(2, '0')}`,
    position,
    config,
  };
}
