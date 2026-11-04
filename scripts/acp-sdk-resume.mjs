// acp-sdk-resume.mjs —— 验证 `session/resume`：跨进程恢复历史。
//
// 流程：
//   进程 A：建会话 → 让模型记住一个暗号 → 关会话 → 退出
//   进程 B：用同一个 sessionId resume → 问"暗号是什么" → 答案里必须出现暗号
//
// 这证明历史是从 v4 文件里读回来的，不是内存残留。
//
// 本机运行：
//   ELECTRON_RUN_AS_NODE=1 "D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe" scripts/acp-sdk-resume.mjs
import { spawn } from 'node:child_process';
import { Readable, Writable } from 'node:stream';

const SDK = 'file:///D:/DeepSeek%20Harness/Desktop/resources/app.asar/dsh/node_modules/@agentclientprotocol/sdk/dist/acp.js';
const { ClientSideConnection, ndJsonStream, PROTOCOL_VERSION } = await import(SDK);

const BIN = process.env.DSHD_BIN || 'tmp/DSH GO.exe';
const SECRET = 'BLUE-FALCON-42';

function connect() {
  const proc = spawn(BIN, ['acp'], { stdio: ['pipe', 'pipe', 'pipe'] });
  proc.stderr.on('data', (b) => process.stderr.write(`[agent] ${b}`));
  const stream = ndJsonStream(Writable.toWeb(proc.stdin), Readable.toWeb(proc.stdout));
  const texts = [];
  const conn = new ClientSideConnection(() => ({
    sessionUpdate: async (params) => {
      const u = params.update;
      if (u.sessionUpdate === 'agent_message_chunk') texts.push(u.content?.text ?? '');
    },
    requestPermission: async () => ({ outcome: { outcome: 'selected', optionId: 'allow-once' } }),
  }), stream);
  return { proc, conn, texts };
}

const cwd = process.cwd();

// ---- 进程 A：写入暗号 ----
const a = connect();
await a.conn.initialize({ protocolVersion: PROTOCOL_VERSION, clientCapabilities: {} });
const sess = await a.conn.newSession({ cwd, mcpServers: [] });
const sid = sess.sessionId;
console.log(`[A] session/new: ${sid}`);

const r1 = await a.conn.prompt({
  sessionId: sid,
  prompt: [{ type: 'text', text: `记住这个暗号：${SECRET}。只需回复 OK。` }],
});
console.log(`[A] stopReason=${r1.stopReason} 回答=${JSON.stringify(a.texts.join('').slice(0, 40))}`);

await a.conn.closeSession({ sessionId: sid }).catch(() => {});
a.proc.stdin.end();
a.proc.kill();
await new Promise((r) => setTimeout(r, 400));

// ---- 进程 B：resume 后追问 ----
const b = connect();
await b.conn.initialize({ protocolVersion: PROTOCOL_VERSION, clientCapabilities: {} });
const resumed = await b.conn.resumeSession({ sessionId: sid, cwd, mcpServers: [] });
console.log(`[B] session/resume 返回: ${JSON.stringify(resumed).slice(0, 80)}`);

const r2 = await b.conn.prompt({
  sessionId: sid,
  prompt: [{ type: 'text', text: '刚才我让你记住的暗号是什么？只回答暗号本身。' }],
});
const answer = b.texts.join('');
console.log(`[B] stopReason=${r2.stopReason}`);
console.log(`[B] 回答: ${JSON.stringify(answer.slice(0, 120))}`);

b.proc.stdin.end();
b.proc.kill();

if (!answer.includes(SECRET)) {
  console.error(`FAIL: 回答里没有暗号 ${SECRET} —— 历史没恢复`);
  process.exit(1);
}
console.log(`PASS: 跨进程恢复成功，暗号出现在答案里`);
process.exit(0);
