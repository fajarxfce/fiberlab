// Test-only GenieACS/MongoDB fixture. These are not Fiberlab runtime dependencies.
// npm install --prefix artifacts/acs-tools --no-audit --no-fund --ignore-scripts \
//   genieacs@1.2.16 mongodb@4 mongodb-memory-server-core@10
const path = require('node:path');
const fs = require('node:fs');
const { spawn } = require('node:child_process');
const { createRequire } = require('node:module');
const dependencies = createRequire(path.resolve('artifacts/acs-tools/package.json'));
const { MongoMemoryServer } = dependencies('mongodb-memory-server-core');
const { MongoClient } = dependencies('mongodb');
const children = [];
let mongo;
let stopping = false;
async function stop(code = 0) {
  if (stopping) return;
  stopping = true;
  for (const child of children) child.kill('SIGTERM');
  await Promise.all(children.map(child => new Promise(resolve => {
    if (child.exitCode !== null) return resolve();
    child.once('exit', resolve);
    setTimeout(() => { child.kill('SIGKILL'); resolve(); }, 5000).unref();
  })));
  if (mongo) await mongo.stop();
  process.exit(code);
}
process.on('SIGINT', () => stop());
process.on('SIGTERM', () => stop());
(async () => {
  process.env.MONGOMS_DISTRO = 'ubuntu-22.04';
  mongo = await MongoMemoryServer.create({ binary: { version: '7.0.16', downloadDir: path.resolve('artifacts/acs-tools/mongodb') }, instance: { ip: '127.0.0.1', port: 27079, dbName: 'fiberlab_acs_test' } });
  const uri = mongo.getUri('fiberlab_acs_test');
  const client = await MongoClient.connect(uri);
  await client.db().collection('config').insertMany([
    { _id: 'cwmp.auth', value: 'AUTH("fiberlab-test", "fiberlab-test-password")' },
    { _id: 'cwmp.connectionRequestAuth', value: 'AUTH("fiberlab-cr", "fiberlab-cr-password")' },
  ]);
  await client.close();
  const env = { ...process.env, GENIEACS_MONGODB_CONNECTION_URL: uri,
    GENIEACS_CWMP_INTERFACE: '127.0.0.1', GENIEACS_CWMP_PORT: '17547', GENIEACS_CWMP_WORKER_PROCESSES: '1',
    GENIEACS_NBI_INTERFACE: '127.0.0.1', GENIEACS_NBI_PORT: '17557', GENIEACS_NBI_WORKER_PROCESSES: '1',
    GENIEACS_LOG_FORMAT: 'json', GENIEACS_ACCESS_LOG_FORMAT: 'json',
  };
  for (const service of ['cwmp', 'nbi']) {
    const log = fs.openSync(path.resolve(`artifacts/genieacs-${service}.log`), 'w', 0o600);
    const child = spawn(process.execPath, [path.resolve(`artifacts/acs-tools/node_modules/genieacs/bin/genieacs-${service}`)], { env, stdio: ['ignore', log, log] });
    children.push(child); fs.closeSync(log);
    child.on('exit', code => { if (!stopping) { console.error(`GenieACS ${service} exited ${code}`); stop(1); } });
  }
  for (let i = 0; i < 100; i++) {
    try { const res = await fetch('http://127.0.0.1:17557/devices'); if (res.ok) { console.log('GenieACS fixture ready · CWMP 127.0.0.1:17547 · NBI 127.0.0.1:17557'); return; } } catch {}
    await new Promise(resolve => setTimeout(resolve, 200));
  }
  throw new Error('GenieACS fixture did not become ready');
})().catch(error => { console.error(error); stop(1); });
