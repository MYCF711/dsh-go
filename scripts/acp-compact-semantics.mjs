// 压缩的语义回归：压完之后，模型还记不记得压缩前提过的暗号？
//
// 结构对不对（事件数、遮蔽列表）由 Go 测试保证；这里只验一件事：
// **压缩有没有把关键信息压没**。
//
// 用法（本机需要 ELECTRON_RUN_AS_NODE=1 指向 DSH 自带的 node）：
//   ELECTRON_RUN_AS_NODE=1 "D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe" \
//     scripts/acp-compact-semantics.mjs
//
// 退出码 0 = 语义保住了。

import { spawn } from 'node:child_process';
import { Readable, Writable } from 'node:stream';
import { rmSync } from 'node:fs';

const SDK = 'file:///D:/DeepSeek%20Harness/Desktop/resources/app.asar/dsh/node_modules/@agentclientprotocol/sdk/dist/acp.js';
const { ClientSideConnection, ndJsonStream, PROTOCOL_VERSION } = await import(SDK);

const HOME = process.env.COMPACT_HOME || 'tmp/compact-semantics-home';
const SECRET = 'COMPACT-KEY-7';
const BIN = process.env.DSHD_BIN || 'tmp/dshd.exe';

rmSync(HOME, { recursive: true, force: true });

const proc = spawn(BIN, ['acp', '--home', HOME, '--workspace-bytes', '0', '--auto-compact', process.env.AUTO_COMPACT || '1'], {
  stdio: ['pipe', 'pipe', 'pipe'],
});
let compressed = 0;
proc.stderr.on('data', (b) => {
  const s = b.toString();
  if (s.includes('自动压缩：')) compressed++;
  process.stderr.write('[agent] ' + s);
});

const texts = [];
const stream = ndJsonStream(Writable.toWeb(proc.stdin), Readable.toWeb(proc.stdout));
const conn = new ClientSideConnection(() => ({
  sessionUpdate: async (p) => {
    const u = p.update;
    if (u.sessionUpdate === 'agent_message_chunk') texts.push(u.content?.text ?? '');
  },
  requestPermission: async () => ({ outcome: { outcome: 'selected', optionId: 'allow-once' } }),
}), stream);

await conn.initialize({ protocolVersion: PROTOCOL_VERSION, clientCapabilities: {} });
const { sessionId } = await conn.newSession({ cwd: process.cwd(), mcpServers: [] });

async function say(prompt) {
  const before = texts.length;
  await conn.prompt({ sessionId, prompt: [{ type: 'text', text: prompt }] });
  return texts.slice(before).join('');
}

await say(`记住这个暗号：${SECRET}。之后我会问你。现在只回复 ok。`);
// 灌几轮填充，让压缩有东西可压。
for (const f of ['复述数字 1 到 5', '复述字母 a 到 e', '随便说一句话']) {
  await say(f);
}

const answer = await say('刚才我让你记住的暗号是什么？只回答暗号本身。');

proc.stdin.end();
proc.kill();

const ok = answer.includes(SECRET) && compressed > 0;
console.log(`压缩触发次数: ${compressed}`);
console.log(`回答: ${JSON.stringify(answer.slice(0, 80))}`);
console.log(ok ? 'PASS: 压缩后仍记得暗号' : 'FAIL: 压缩把关键信息丢了，或者压根没触发');
process.exit(ok ? 0 : 1);
