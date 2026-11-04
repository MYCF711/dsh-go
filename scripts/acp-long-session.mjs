// 长会话实测：10 轮以上，看压缩怎么衰减、暗号还记不记得。
//
// 和 acp-compact-semantics.mjs 的区别：那个只跑 3 轮（够触发压缩就行），
// 这个跑 TURNS 轮，并且每轮带一次工具调用（真实会话里工具输出才是大头），
// 同时记录每轮的 usage_update，最后画出一条成本曲线。
//
// 用法：
//   ELECTRON_RUN_AS_NODE=1 "D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe" \
//     scripts/acp-long-session.mjs
//
// 环境变量：TURNS（默认 12）、AUTO_COMPACT（默认 4000）、COMPACT_HOME

import { spawn } from 'node:child_process';
import { Readable, Writable } from 'node:stream';
import { rmSync, writeFileSync, mkdirSync } from 'node:fs';

const SDK = 'file:///D:/DeepSeek%20Harness/Desktop/resources/app.asar/dsh/node_modules/@agentclientprotocol/sdk/dist/acp.js';
const { ClientSideConnection, ndJsonStream, PROTOCOL_VERSION } = await import(SDK);

const HOME = process.env.COMPACT_HOME || 'tmp/long-session-home';
const TURNS = Number(process.env.TURNS || 12);
const AUTO = process.env.AUTO_COMPACT || '4000';
const SECRET = 'LONG-SESSION-KEY-42';
// 每轮读多少行：这个数直接决定历史增长速度，也就决定压缩有没有收益。
const LINES = Number(process.env.LINES_PER_TURN || 6);
const BIN = process.env.DSHD_BIN || 'tmp/DSH GO.exe';

rmSync(HOME, { recursive: true, force: true });
mkdirSync('tmp', { recursive: true });
// 让每轮的工具输出有东西可读。
writeFileSync('tmp/long-session-filler.txt', Array.from({ length: 4000 }, (_, i) => `第 ${i + 1} 行：这是填充内容，用来制造工具输出。`).join('\n'));

const proc = spawn(BIN, ['acp', '--home', HOME, '--workspace-bytes', '0', '--auto-compact', AUTO], {
  stdio: ['pipe', 'pipe', 'pipe'],
});
let compressed = 0;
proc.stderr.on('data', (b) => {
  const s = b.toString();
  for (const line of s.split('\n')) {
    if (line.includes('自动压缩：')) compressed++;
  }
  if (process.env.VERBOSE) process.stderr.write('[agent] ' + s);
});

const texts = [];
const usage = [];
const stream = ndJsonStream(Writable.toWeb(proc.stdin), Readable.toWeb(proc.stdout));
const conn = new ClientSideConnection(() => ({
  sessionUpdate: async (p) => {
    const u = p.update;
    if (u.sessionUpdate === 'agent_message_chunk') texts.push(u.content?.text ?? '');
    if (u.sessionUpdate === 'usage_update') usage.push({ used: u.used, size: u.size });
  },
  requestPermission: async () => ({ outcome: { outcome: 'selected', optionId: 'allow-once' } }),
}), stream);

await conn.initialize({ protocolVersion: PROTOCOL_VERSION, clientCapabilities: {} });
const { sessionId } = await conn.newSession({ cwd: process.cwd(), mcpServers: [] });

async function say(prompt) {
  const before = texts.length;
  const u0 = usage.length;
  const t0 = Date.now();
  await conn.prompt({ sessionId, prompt: [{ type: 'text', text: prompt }] });
  const answer = texts.slice(before).join('');
  const last = usage.slice(u0).at(-1);
  return { answer, used: last?.used ?? null, ms: Date.now() - t0 };
}

const rows = [];

// 第 1 轮：种下暗号。
rows.push({ turn: 1, ...(await say(`记住这个暗号：${SECRET}。之后我会问你。现在只回复 ok。`)) });

// 中间轮：每轮真的调一次工具，制造真实规模的历史。
// 用 read 读一个小文件，避免 bash（bash 的输出边界另有测试管）。
for (let i = 2; i <= TURNS - 1; i++) {
  rows.push({
    turn: i,
    ...(await say(`读 tmp/long-session-filler.txt 的第 ${i} 到第 ${i + LINES - 1} 行，然后把最后一行原样复述给我。`)),
  });
}

// 最后一轮：问暗号。
const last = await say('我在第一轮让你记住的暗号是什么？只回答暗号本身。');
rows.push({ turn: TURNS, ...last });

proc.stdin.end();
proc.kill();

console.log('轮次 | used(token) | 耗时ms | 回答摘要');
for (const r of rows) {
  console.log(`${String(r.turn).padStart(4)} | ${String(r.used ?? '-').padStart(10)} | ${String(r.ms).padStart(6)} | ${JSON.stringify(r.answer.slice(0, 40))}`);
}
console.log(`\n压缩触发次数: ${compressed}`);
const remembered = last.answer.includes(SECRET);
console.log(`最终回答: ${JSON.stringify(last.answer.slice(0, 80))}`);
console.log(remembered ? 'PASS: 长会话里暗号还在' : 'FAIL: 长会话把暗号丢了');
process.exit(remembered ? 0 : 1);
