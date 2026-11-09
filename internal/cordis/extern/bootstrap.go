package extern

// bootstrapJS 是外部 node 进程的引导脚本。
//
// 它**不是**宿主里的 JS 引擎：这段文本在启动时写到临时目录的一个 .cjs 文件，
// 由用户机器上的 node 执行。宿主自己一行 JS 也不解释。
//
// 脚本只做三件事：读 stdin 的一行一帧、按 method 分派、把结果写回 stdout。
// 契约测试用的方法（echo / sleep / boom / spawnChild）在这里实现；C3 的真实
// 插件加载会把它替换成"Cordis 插件宿主"，但**协议不变**。
const bootstrapJS = `// cordis-extern bootstrap —— 非官方 wire（见 internal/cordis/extern/doc.go）
'use strict';
const readline = require('readline');
const { spawn } = require('child_process');
const fs = require('fs');
const path = require('path');
let loaded = null; // {dir, pkg, methods}

function send(obj) {
  process.stdout.write(JSON.stringify(obj) + '\n');
}

function reply(id, result) {
  send({ jsonrpc: '2.0', id: id, result: result });
}

function fail(id, code, message, data) {
  const err = { code: code, message: message };
  if (data !== undefined) err.data = data;
  send({ jsonrpc: '2.0', id: id, error: err });
}

const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });

rl.on('line', (line) => {
  const text = line.trim();
  if (!text) return;
  let req;
  try {
    req = JSON.parse(text);
  } catch (e) {
    fail(null, -32700, 'parse error: ' + String(e && e.message || e));
    return;
  }
  const id = req && req.id;
  const method = req && req.method;
  const params = (req && req.params) || {};
  try {
    switch (method) {
      case 'echo':
        reply(id, { echo: params.text === undefined ? null : params.text,
                    n: params.n === undefined ? null : params.n });
        return;
      case 'sleep': {
        const ms = Math.max(0, Number(params.ms) || 0);
        setTimeout(() => reply(id, { sleptMs: ms }), ms);
        return;
      }
      case 'boom':
        fail(id, -32000, String(params.why || 'plugin error'), { kind: 'plugin-error' });
        return;
      case 'spawnChild': {
        const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000);'], {
          stdio: 'ignore', windowsHide: true,
        });
        reply(id, { childPid: child.pid });
        return;
      }
      case 'pid':
        reply(id, { pid: process.pid });
        return;
      case 'load':
      case 'runHostHalf': {
        const dir = path.resolve(String(params.dir || ''));
        const pkg = JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'));
        const main = path.resolve(dir, String(pkg.main || 'host.cjs'));
        if (!main.startsWith(dir + path.sep) && main !== dir) {
          fail(id, -32602, 'entry 越出插件目录', { dir: dir });
          return;
        }
        const mod = require(main);
        const methods = (mod && mod.methods) ? mod.methods : mod;
        if (!methods || typeof methods !== 'object') {
          fail(id, -32603, '插件没有导出方法表', { dir: dir });
          return;
        }
        loaded = { dir: dir, pkg: pkg, methods: methods };
        reply(id, { name: String(pkg.name || ''), version: String(pkg.version || ''),
                    main: main, methods: Object.keys(methods), pid: process.pid });
        return;
      }
      case 'getClientCode': {
        const dir = path.resolve(String(params.dir || ''));
        const rel = String(params.file || 'client/client.js');
        const file = path.resolve(dir, rel);
        if (!file.startsWith(dir + path.sep)) {
          fail(id, -32602, 'client 文件越出插件目录', { file: rel });
          return;
        }
        const code = fs.readFileSync(file);
        reply(id, { path: file, codeBase64: code.toString('base64'), bytes: code.length });
        return;
      }
      case 'invoke': {
        if (!loaded) { fail(id, -32000, '插件尚未 load', { method: params.method }); return; }
        const name = String(params.method || '');
        const fn = loaded.methods[name];
        if (typeof fn !== 'function') {
          fail(id, -32601, '插件方法不存在: ' + name, { method: name });
          return;
        }
        Promise.resolve(fn(params.args || {})).then((result) => {
          const payload = { result: result === undefined ? null : result, pid: process.pid,
                            requestId: params.requestId === undefined ? null : params.requestId };
          reply(id, payload);
        }).catch((e) => fail(id, -32000, String(e && e.message || e),
                             { method: name, kind: 'plugin-error' }));
        return;
      }
      case 'settleUserRun':
        reply(id, { requestId: params.requestId === undefined ? null : params.requestId,
                    pid: process.pid, exitCode: process.exitCode === undefined ? 0 : process.exitCode,
                    settledAt: Date.now() });
        return;
      case 'resolveInspectQuery':
        reply(id, loaded ? { name: String(loaded.pkg.name || ''),
                             version: String(loaded.pkg.version || ''),
                             methods: Object.keys(loaded.methods), pid: process.pid } : null);
        return;
      default:
        fail(id, -32601, 'method not found: ' + String(method));
        return;
    }
  } catch (e) {
    fail(id, -32603, String(e && e.message || e));
  }
});

rl.on('close', () => { process.exit(0); });
`
