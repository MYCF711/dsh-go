// 端到端回归：把 ACP 的方法挨个走一遍，看有没有哪条链路断了。
//
// 一个进程里跑完 new / prompt / set_mode / load / fork / delete，
// 再起第二个进程验 resume（跨进程是这条链路最容易断的地方）。
//
// 用法：
//   ELECTRON_RUN_AS_NODE=1 "D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe" \
//     scripts/e2e-regression.mjs
//
// 退出码 0 = 全通过。

import { spawn } from 'node:child_process';
import { Readable, Writable } from 'node:stream';
import { rmSync, readdirSync } from 'node:fs';

const SDK = 'file:///D:/DeepSeek%20Harness/Desktop/resources/app.asar/dsh/node_modules/@agentclientprotocol/sdk/dist/acp.js';
const { ClientSideConnection, ndJsonStream, PROTOCOL_VERSION } = await import(SDK);

const HOME = 'tmp/e2e-home';
const CWD = 'D:/DeepSeek Harness/default-workspace/DSH-GO';
const BIN = 'tmp/dshd.exe';
const results = [];

function record(name, ok, detail = '') {
  results.push({ name, ok, detail });
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? '  — ' + detail : ''}`);
}

async function connect(extraArgs = []) {
  const proc = spawn(BIN, ['acp', '--home', HOME, '--workspace-bytes', '0', ...extraArgs], {
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  proc.on('error', (e) => console.error('[spawn]', e.message));
  proc.stderr.on('data', (b) => process.stderr.write('[agent] ' + b));

  const seen = { tools: [], texts: [], thoughts: [], usage: null, asked: 0 };
  const stream = ndJsonStream(Writable.toWeb(proc.stdin), Readable.toWeb(proc.stdout));
  const conn = new ClientSideConnection(() => ({
    sessionUpdate: async (p) => {
      const u = p.update;
      if (u.sessionUpdate === 'tool_call') seen.tools.push(u.title);
      if (u.sessionUpdate === 'agent_message_chunk') seen.texts.push(u.content?.text ?? '');
      if (u.sessionUpdate === 'agent_thought_chunk') seen.thoughts.push(u.content?.text ?? '');
      if (u.sessionUpdate === 'usage_update') seen.usage = { used: u.used, size: u.size };
    },
    requestPermission: async () => {
      seen.asked++;
      return { outcome: { outcome: 'selected', optionId: 'allow-once' } };
    },
  }), stream);
  await conn.initialize({ protocolVersion: PROTOCOL_VERSION, clientCapabilities: {} });
  const done = async () => {
    try { proc.stdin.end(); } catch {}
    await new Promise((r) => setTimeout(r, 250));
    try { proc.kill(); } catch {}
  };
  return { conn, seen, done };
}

// findSessions 递归找会话文件（不猜 projectKey 的编码规则）。
function findSessions(root) {
  const out = [];
  const walk = (dir) => {
    let entries;
    try { entries = readdirSync(dir, { withFileTypes: true }); } catch { return; }
    for (const e of entries) {
      const p = `${dir}/${e.name}`;
      if (e.isDirectory()) walk(p);
      else if (e.name === 'session.v4.jsonl.zstd') out.push(p);
    }
  };
  walk(root);
  return out;
}

rmSync(HOME, { recursive: true, force: true });

// ── 进程 A ─────────────────────────────────────────────
const A = await connect();
let sid;
try {
  // 1. session/new（带 mode）
  const created = await A.conn.newSession({ cwd: CWD, mcpServers: [], mode: 'default' });
  sid = created.sessionId;
  record('session/new + mode', !!sid, sid && sid.slice(0, 12));

  // 2. session/prompt：要有正文、要有工具调用、要发 usage_update
  const nTools = A.seen.tools.length;
  const r1 = await A.conn.prompt({
    sessionId: sid,
    prompt: [{ type: 'text', text: '用 bash 执行 echo E2E-OK，然后告诉我输出。' }],
  });
  record('session/prompt 结束理由', r1.stopReason === 'end_turn', r1.stopReason);
  record('prompt 产生工具调用', A.seen.tools.length > nTools, JSON.stringify(A.seen.tools.slice(nTools)));
  record('prompt 产生正文', A.seen.texts.join('').length > 0, `${A.seen.texts.join('').length} 字符`);
  record('审批链路被走过', A.seen.asked > 0, `${A.seen.asked} 次`);
  record('usage_update 到达', !!A.seen.usage, JSON.stringify(A.seen.usage));

  // 3. session/set_mode：未知模式必须报错
  let rejected = false;
  try { await A.conn.setSessionMode({ sessionId: sid, modeId: 'nope-mode' }); }
  catch { rejected = true; }
  record('set_mode 拒绝未知模式', rejected);

  // 4. session/load：重放历史
  const before = A.seen.texts.length;
  await A.conn.loadSession({ sessionId: sid, cwd: CWD, mcpServers: [] });
  record('session/load 重放历史', A.seen.texts.length > before, `+${A.seen.texts.length - before} 块`);

  // 5. session/fork：子会话能接着聊
  const fork = await A.conn.unstable_forkSession({ sessionId: sid, cwd: CWD, mcpServers: [] });
  record('session/fork', !!fork.sessionId, fork.sessionId && fork.sessionId.slice(0, 12));

  // 6. 会话文件确实落盘了（真的去找，不许写「恒真」的断言）
  const files = findSessions(HOME);
  record('会话文件落盘', files.length > 0, `${files.length} 个`);
} catch (e) {
  record('进程 A 全流程', false, e.message);
}
await A.done();

// ── 取消链路：工具跑到一半取消 ──────────────────────────
// 这是最容易断的一条：prompt 在飞、bash 在 sleep、cancel 进来。
const K = await connect();
try {
  const { sessionId: kid } = await K.conn.newSession({ cwd: CWD, mcpServers: [], mode: 'default' });

  // 不 await：prompt 必须异步跑起来，否则 cancel 永远进不来
  const pending = K.conn.prompt({
    sessionId: kid,
    prompt: [{ type: 'text', text: '用 bash 执行 sleep 20，然后告诉我睡完了。' }],
  });
  await new Promise((r) => setTimeout(r, 1500));   // 等它真的开跑
  const t0 = Date.now();
  await K.conn.cancel({ sessionId: kid });
  const stopped = await pending;
  const elapsed = Date.now() - t0;

  record('cancel 让 prompt 以 cancelled 结束', stopped.stopReason === 'cancelled', stopped.stopReason);
  record('cancel 是快的（<5s）', elapsed < 5000, `${elapsed} ms`);

  // 取消之后会话必须还能用
  const n = K.seen.tools.length;
  const again = await K.conn.prompt({ sessionId: kid, prompt: [{ type: 'text', text: '说一句 ok。' }] });
  record('取消后会话仍可用', again.stopReason === 'end_turn', again.stopReason);
} catch (e) {
  record('取消链路', false, e.message);
}
await K.done();

// ── 进程 B：resume 跨进程 ──────────────────────────────
const B = await connect();
try {
  await B.conn.resumeSession({ sessionId: sid, cwd: CWD, mcpServers: [] });
  const n = B.seen.tools.length;
  await B.conn.prompt({ sessionId: sid, prompt: [{ type: 'text', text: '用 bash 执行 echo E2E-RESUME。' }] });
  record('session/resume 跨进程可用', B.seen.tools.length > n, JSON.stringify(B.seen.tools.slice(n)));
} catch (e) {
  record('session/resume 跨进程可用', false, e.message);
}
await B.done();

// ── 进程 C：delete ────────────────────────────────────
// 注意：fork 出来的子会话也占一个文件，所以只能断言"少了一个"，
// 不能断言"全都消失了"。
const beforeDelete = findSessions(HOME);
const C = await connect();
try {
  await C.conn.deleteSession({ sessionId: sid });
  record('session/delete', true, '已删除');
} catch (e) {
  record('session/delete', false, e.message);
}
await C.done();

const afterDelete = findSessions(HOME);
record('delete 只删掉目标会话',
  afterDelete.length === beforeDelete.length - 1,
  `${beforeDelete.length} → ${afterDelete.length}（fork 出来的还在，是对的）`);

const failed = results.filter((r) => !r.ok);
console.log('');
console.log(`合计 ${results.length} 项，失败 ${failed.length} 项`);
process.exit(failed.length === 0 ? 0 : 1);
