// acp-sdk-client.mjs —— 用官方 SDK（@agentclientprotocol/sdk 1.4.0）当真实客户端，
// 连我们自己的 `dshd acp`。这是 P0 判据①的验收方式：不是"我们说兼容"，而是第三方库能连。
//
// 本机运行（SDK 在 asar 里，只有 Electron 的 Node 模式读得到）：
//   ELECTRON_RUN_AS_NODE=1 "D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe" \
//       scripts/acp-sdk-client.mjs "<要说的话>"
import { spawn } from 'node:child_process';
import { Readable, Writable } from 'node:stream';

const SDK = 'file:///D:/DeepSeek%20Harness/Desktop/resources/app.asar/dsh/node_modules/@agentclientprotocol/sdk/dist/acp.js';
const { ClientSideConnection, ndJsonStream, PROTOCOL_VERSION } = await import(SDK);

const BIN = process.env.DSHD_BIN || 'tmp/dshd.exe';
const prompt = process.argv[2] || '用一句话说明你能做什么。';

const proc = spawn(BIN, ['acp'], { stdio: ['pipe', 'pipe', 'pipe'] });
proc.stderr.on('data', (b) => process.stderr.write(`[agent stderr] ${b}`));

const stream = ndJsonStream(
  Writable.toWeb(proc.stdin),
  Readable.toWeb(proc.stdout),
);

const chunks = [];
const perms = [];
const client = new ClientSideConnection(() => ({
  sessionUpdate: async (params) => {
    const u = params.update;
    if (u.sessionUpdate === 'agent_thought_chunk' || u.sessionUpdate === 'agent_message_chunk') {
      chunks.push({ kind: u.sessionUpdate, text: u.content?.text ?? '' });
    } else if (u.sessionUpdate === 'tool_call') {
      chunks.push({ kind: 'tool_call', title: u.title, input: u.rawInput });
    } else if (u.sessionUpdate === 'tool_call_update') {
      chunks.push({ kind: 'tool_call_update', status: u.status });
    } else if (u.sessionUpdate === 'usage_update') {
      chunks.push({ kind: 'usage_update', used: u.used, size: u.size });
    }
  },
  requestPermission: async (params) => {
    const title = params?.toolCall?.title ?? '?';
    const deny = process.env.ACP_DENY === '1';
    perms.push({ title, deny });
    return deny
      ? { outcome: { outcome: 'selected', optionId: 'reject-once' } }
      : { outcome: { outcome: 'selected', optionId: 'allow-once' } };
  },
}), stream);

const init = await client.initialize({ protocolVersion: PROTOCOL_VERSION, clientCapabilities: {} });
console.log(`initialize: ${init.agentInfo?.name} ${init.agentInfo?.version} protocol=${init.protocolVersion}`);

const sess = await client.newSession({ cwd: process.cwd(), mcpServers: [] });
console.log(`session/new: ${sess.sessionId} configOptions=${(sess.configOptions ?? []).length}`);

const t0 = Date.now();
const res = await client.prompt({
  sessionId: sess.sessionId,
  prompt: [{ type: 'text', text: prompt }],
});
const ms = Date.now() - t0;

const think = chunks.filter((c) => c.kind === 'agent_thought_chunk');
const text = chunks.filter((c) => c.kind === 'agent_message_chunk');
const tools = chunks.filter((c) => c.kind === 'tool_call');

console.log(`prompt: stopReason=${res.stopReason} 用时=${ms}ms`);
console.log(`  reasoning 分片=${think.length}（${think.reduce((n, c) => n + c.text.length, 0)} 字）`);
console.log(`  text 分片=${text.length}（${text.reduce((n, c) => n + c.text.length, 0)} 字）`);
console.log(`  工具调用=${tools.length} ${tools.map((t) => t.title).join(',')}`);
console.log(`  审批请求=${perms.length} ${perms.map((p) => `${p.title}:${p.deny ? 'deny' : 'allow'}`).join(',')}`);
const usage = chunks.filter((c) => c.kind === 'usage_update');
if (usage.length) console.log(`  usage_update: used=${usage.at(-1).used} size=${usage.at(-1).size}`);
if (think.length) console.log(`  reasoning 首片: ${JSON.stringify(think[0].text.slice(0, 80))}`);
if (text.length) console.log(`  回答: ${text.map((c) => c.text).join('').slice(0, 200)}`);

await client.closeSession({ sessionId: sess.sessionId }).catch(() => {});
proc.stdin.end();
proc.kill();

if (res.stopReason !== 'end_turn') { console.error('FAIL: stopReason 不是 end_turn'); process.exit(1); }
if (text.length === 0) { console.error('FAIL: 没有收到任何 agent_message_chunk'); process.exit(1); }
process.exit(0);
