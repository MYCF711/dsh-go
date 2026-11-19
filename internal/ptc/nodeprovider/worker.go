package nodeprovider

// workerSource 是外部 node 进程的**数据文件**内容。
//
// 它**不是**宿主里的 JS 引擎：这段文本在 New 时写到临时目录的一个 .cjs 文件，
// 由用户机器上的 node 执行。宿主自己不解释一行 JS（R6 的词面就在这里）。
//
// 它只做五件事：读一行一帧、建绑定命名空间、eval 一次程序、把日志流出来、
// 把完成值或失败结算成帧。控制通道与程序输出共用 stdout，所以程序（以及任何
// 直接写 stdout 的库）的字节统统被重定向成 log 帧——否则会把协议冲掉。
const workerSource = `'use strict';
// dsh-ptc worker —— 外部进程，按次起停。宿主不解释本文件。
const readline = require('node:readline');
const util = require('node:util');

const realWrite = process.stdout.write.bind(process.stdout);

let evalID = 0;
let maxOutput = 0;
let outBytes = 0;
let settled = false;
let started = false;

function sendRaw(obj, onFlushed) {
  const line = JSON.stringify(obj) + '\n';
  if (onFlushed) {
    realWrite(line, function () { process.exit(0); });
  } else {
    realWrite(line);
  }
}

function describeError(err) {
  if (!err) return 'unknown error';
  let text;
  if (err instanceof Error) {
    text = (err.name || 'Error') + ': ' + (err.message || '');
    const stack = typeof err.stack === 'string' ? err.stack.split('\n')[0] : '';
    if (stack && stack.indexOf(text) === -1) text += ' (' + stack + ')';
  } else {
    try { text = typeof err === 'string' ? err : util.inspect(err, { depth: 2 }); }
    catch (e) { text = String(err); }
  }
  return text;
}

function emitLog(args) {
  if (settled) return;
  const text = Array.prototype.map.call(args, function (v) {
    if (typeof v === 'string') return v;
    try { return util.inspect(v, { depth: 4, breakLength: Infinity, maxArrayLength: 100, maxStringLength: 10000 }); }
    catch (e) { return String(v); }
  }).join(' ');
  outBytes += Buffer.byteLength(text) + 1;
  if (maxOutput > 0 && outBytes > maxOutput) {
    fail('output-limit', 'captured output exceeded ' + maxOutput + ' bytes');
    return;
  }
  sendRaw({ t: 'log', id: evalID, text: text });
}

// 程序（和它用的库）写到 stdout/stderr 的字节都进日志帧：控制通道就在 stdout 上。
process.stdout.write = function (chunk) {
  emitLog([typeof chunk === 'string' ? chunk : Buffer.from(chunk).toString('utf8')]);
  return true;
};
process.stderr.write = process.stdout.write;

const consoleShim = {
  log: function () { emitLog(arguments); },
  info: function () { emitLog(arguments); },
  warn: function () { emitLog(arguments); },
  error: function () { emitLog(arguments); },
  debug: function () { emitLog(arguments); },
  trace: function () { emitLog(arguments); },
};

function fail(kind, message) {
  if (settled) return;
  settled = true;
  sendRaw({ t: 'fail', id: evalID, kind: kind, message: String(message) }, true);
}

function done(hasValue, value) {
  if (settled) return;
  settled = true;
  const out = { t: 'done', id: evalID, has: hasValue === true };
  if (hasValue === true) out.value = value;
  sendRaw(out, true);
}

// assertLossless 复刻官方的无损 JSON 边界：函数/symbol/undefined/BigInt/非有限数/
// 循环引用/稀疏数组/异形原型一律不算"能穿的完成值"。
function assertLossless(value, pathStr, seen) {
  if (value === null) return;
  const t = typeof value;
  if (t === 'boolean' || t === 'string') return;
  if (t === 'number') {
    if (!Number.isFinite(value)) throw new Error(pathStr + ': ' + String(value) + ' is not a finite JSON number');
    return;
  }
  if (t === 'undefined') throw new Error(pathStr + ': undefined is not a JSON value');
  if (t === 'bigint') throw new Error(pathStr + ': bigint is not a JSON value');
  if (t === 'function' || t === 'symbol') throw new Error(pathStr + ': ' + t + ' is not a JSON value');
  if (seen.has(value)) throw new Error(pathStr + ': circular reference');
  seen.add(value);
  if (Array.isArray(value)) {
    for (let i = 0; i < value.length; i += 1) {
      if (!(i in value)) throw new Error(pathStr + '[' + i + ']: sparse array hole is not a JSON value');
      assertLossless(value[i], pathStr + '[' + i + ']', seen);
    }
    seen.delete(value);
    return;
  }
  const proto = Object.getPrototypeOf(value);
  if (proto !== Object.prototype && proto !== null) {
    const name = proto && proto.constructor && proto.constructor.name ? proto.constructor.name : 'unknown';
    throw new Error(pathStr + ': exotic prototype ' + name + ' is not a JSON object');
  }
  const keys = Object.keys(value);
  for (let i = 0; i < keys.length; i += 1) {
    assertLossless(value[keys[i]], pathStr + '.' + keys[i], seen);
  }
  seen.delete(value);
}

function makeErrorClass(spec) {
  function BindingError(member, message) {
    const err = new Error(message);
    err.name = spec.name;
    Object.defineProperty(err, spec.memberNameProperty, {
      value: member, enumerable: true, writable: true, configurable: true,
    });
    return err;
  }
  Object.defineProperty(BindingError, 'name', { value: spec.name });
  return BindingError;
}

const pending = new Map();
let nextCall = 1;

function callHost(ns, member, encoded) {
  const call = nextCall;
  nextCall += 1;
  return new Promise(function (resolve) {
    pending.set(call, resolve);
    sendRaw({ t: 'bind', id: evalID, call: call, ns: ns, fn: member, args: JSON.parse(encoded) });
  });
}

function onBindResult(f) {
  const resolve = pending.get(f.call);
  if (!resolve) return;
  pending.delete(f.call);
  resolve({ ok: f.ok === true, value: f.value, message: f.message });
}

function makeNamespace(ns) {
  const obj = Object.create(null); // 空原型：__proto__ / constructor 只是普通自有属性
  let errClass = null;
  if (ns.errorClass) errClass = makeErrorClass(ns.errorClass);
  const members = ns.members || [];
  for (let i = 0; i < members.length; i += 1) {
    const member = members[i];
    obj[member] = async function () {
      if (arguments.length > 1) {
        throw new TypeError('binding ' + ns.global + '.' + member + ' takes one args value, got ' + arguments.length);
      }
      const payload = arguments.length === 0 || arguments[0] === undefined ? null : arguments[0];
      let encoded;
      try { encoded = JSON.stringify(payload); }
      catch (e) { throw new TypeError('arguments for ' + ns.global + '.' + member + ' are not lossless JSON: ' + describeError(e)); }
      if (encoded === undefined) {
        throw new TypeError('arguments for ' + ns.global + '.' + member + ' are not lossless JSON');
      }
      const reply = await callHost(ns.global, member, encoded);
      if (!reply.ok) {
        const message = reply.message || ('binding ' + ns.global + '.' + member + ' failed');
        if (errClass) throw new errClass(member, message);
        throw new Error(message);
      }
      return reply.value === undefined ? null : reply.value;
    };
  }
  return obj;
}

function buildFunction(program, globals) {
  try {
    const mod = require('node:module');
    if (typeof mod.stripTypeScriptTypes === 'function') {
      const wrapped = '(async function (' + globals.join(', ') + ') {\n' + program + '\n})';
      const stripped = mod.stripTypeScriptTypes(wrapped, { mode: 'strip' });
      return (0, eval)(stripped);
    }
  } catch (e) {
    // 剥类型失败：退回纯 JS 编译，让 AsyncFunction 给出真正的语法错误。
  }
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
  return new AsyncFunction(globals.join(', '), program);
}

async function runEval(f) {
  evalID = f.id || 1;
  maxOutput = f.maxOutputBytes || 0;
  if (f.cwd) {
    try { process.chdir(f.cwd); } catch (e) { /* 宿主已校验过 cwd，这里失败不致命 */ }
  }
  const namespaces = f.namespaces || [];
  const globals = ['console'];
  const values = [consoleShim];
  for (let i = 0; i < namespaces.length; i += 1) {
    globals.push(namespaces[i].global);
    values.push(makeNamespace(namespaces[i]));
  }
  let fn;
  try {
    fn = buildFunction(f.program, globals);
  } catch (e) {
    fail('exception', 'program did not compile: ' + describeError(e));
    return;
  }
  let value;
  try {
    value = await fn.apply(undefined, values);
  } catch (e) {
    fail('exception', describeError(e));
    return;
  }
  if (value === undefined) {
    done(false);
    return;
  }
  try {
    assertLossless(value, 'result', new Set());
  } catch (e) {
    fail('invalid-output', describeError(e));
    return;
  }
  done(true, value);
}

process.on('uncaughtException', function (e) { fail('exception', 'uncaught: ' + describeError(e)); });
process.on('unhandledRejection', function (e) { fail('exception', 'unhandled rejection: ' + describeError(e)); });

const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
rl.on('line', function (line) {
  const text = line.trim();
  if (!text) return;
  let f;
  try {
    f = JSON.parse(text);
  } catch (e) {
    fail('protocol', 'host frame is not valid JSON: ' + describeError(e));
    return;
  }
  if (f.t === 'bindResult') { onBindResult(f); return; }
  if (f.t === 'eval') {
    if (started) { fail('protocol', 'second eval on a per-call worker'); return; }
    started = true;
    runEval(f).catch(function (e) { fail('exception', 'worker internal failure: ' + describeError(e)); });
    return;
  }
  if (f.t === 'cancel') {
    // 单线程里打断不了同步循环：宿主会按 stopGrace 直接终止进程树。
    return;
  }
});

rl.on('close', function () {
  if (!settled) {
    settled = true;
    process.exit(0);
  }
});

sendRaw({ t: 'ready', pid: process.pid, version: process.version });
`
