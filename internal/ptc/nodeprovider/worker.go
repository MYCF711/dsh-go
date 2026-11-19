package nodeprovider

// workerSource 是外部 node 进程的**数据文件**内容。
//
// 它**不是**宿主里的 JS 引擎：这段文本在 New 时写到临时目录的一个 .cjs 文件，
// 由用户机器上的 node 执行。宿主自己不解释一行 JS（R6 的词面就在这里）。
//
// 它做六件事：读一行一帧、建绑定命名空间、按 `format` 选求值器（async 函数体 /
// `node:vm` Script）、把日志流出来、把完成值或失败结算成帧、按 `retainsState`
// 决定结算后进程是否继续活着（scriptPersistence）。
// 控制通道与程序输出共用 stdout，所以程序（以及任何直接写 stdout 的库）的字节
// 统统被重定向成 log 帧——否则会把协议冲掉。
//
// # `node:vm` 在这份文件里是什么、不是什么
//
// `script` 形态用 `vm.createContext` + `new vm.Script(...).runInContext(...)`，
// 与官方 `evaluators/vm-script.ts:32-61` 同形。它买到的是**上下文隔离**
// （独立的 global 对象与 realm；顶层声明可跨次保留），**不是安全边界**：
// 官方 README 原文 "the context is not a security boundary"（见
// `docs/ptc-vm-script.md`）。具体地：这里把外层 `globalThis` 的自有属性描述符
// **原样拷进** context（官方 `vm-script.ts:36-39`），所以程序拿到的 `process` /
// `Buffer` / `fetch` …就是外层真实对象的引用；`require` 不在 globalThis 上因此
// 不可见，但 `await import(...)` 走的仍是本进程的加载器。真正的护栏在进程之外：
// 受管子进程 + 整棵进程树可杀 + elapsed 预算 + 输出/帧/堆上限 + 环境变量擦除。
const workerSource = `'use strict';
// dsh-ptc worker —— 外部进程，按次起停，或按 stateOwner 常驻（scriptPersistence）。
// 宿主不解释本文件。
const readline = require('node:readline');
const util = require('node:util');
const vm = require('node:vm');
const nodePath = require('node:path');
const AsyncLocalStorage = require('node:async_hooks').AsyncLocalStorage;

// evaluationContext 是**输出归属**的判据（官方 worker.ts:2 的 AsyncLocalStorage 同形）：
// 只有"由当前这次求值发起的"字节才算这一轮的输出。常驻会话里后台定时器的回调
// 捕获的是**上一轮**的 store（running=false），于是它的 console.log 会被丢掉——
// 官方 src/execution.ts:130 的 "captured output is discarded" 正是这条，不这样做就会把上一轮的
// 字节混进下一轮。
const evaluationContext = new AsyncLocalStorage();

const realWrite = process.stdout.write.bind(process.stdout);

// STARTUP_ENVIRONMENT_NAMES 是官方 src/environment.ts:5 的同一组：
// 原生可执行文件的搜索路径、Windows 系统路径、临时目录。其余变量在求值前删掉，
// 与官方 clearProgramEnvironment（src/environment.ts:13-19）同形。
// 这不是"沙箱"：它只是把凭据类环境变量从程序视野里拿掉，进程权限一点没变。
const STARTUP_ENVIRONMENT_NAMES = ['PATH', 'PATHEXT', 'SYSTEMROOT', 'WINDIR', 'TEMP', 'TMP'];

function clearProgramEnvironment() {
  const keep = Object.create(null);
  for (let i = 0; i < STARTUP_ENVIRONMENT_NAMES.length; i += 1) {
    keep[STARTUP_ENVIRONMENT_NAMES[i]] = true;
  }
  const names = Object.keys(process.env);
  for (let i = 0; i < names.length; i += 1) {
    if (!keep[names[i].toUpperCase()]) delete process.env[names[i]];
  }
}

let evalID = 0;
let maxOutput = 0;
let outBytes = 0;
let settled = false;
let evalCount = 0;
let programFormat = 'function-body';
let stateRetained = false;
let scriptContext = null;
let activeEvaluation = null;

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
  // 只有当前求值发起的输出算数：结算之后（或上一轮级联下来的回调）一律丢弃。
  const current = evaluationContext.getStore();
  if (settled || current === undefined || current.running !== true) return;
  const text = Array.prototype.map.call(args, function (v) {
    if (typeof v === 'string') return v;
    try { return util.inspect(v, { depth: 4, breakLength: Infinity, maxArrayLength: 100, maxStringLength: 10000 }); }
    catch (e) { return String(v); }
  }).join(' ');
  outBytes += Buffer.byteLength(text) + 1;
  if (maxOutput > 0 && outBytes > maxOutput) {
    fail('output-limit', 'captured output exceeded ' + maxOutput + ' bytes', true);
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

// terminate=true 表示这次失败之后进程也不该继续活着（常驻会话里进程死 = 状态清掉）。
// per-call 会话每次结算都必须退出；常驻会话只在 terminate 时才退出。
function terminating(terminate) {
  return terminate === true || !stateRetained;
}

function settle() {
  if (activeEvaluation !== null) activeEvaluation.running = false;
  activeEvaluation = null;
  settled = true;
}

function fail(kind, message, terminate) {
  if (settled) return;
  settle();
  const frame = { t: 'fail', id: evalID, kind: kind, message: String(message) };
  if (terminating(terminate)) {
    sendRaw(frame, true);
  } else {
    sendRaw(frame, false);
  }
}

function done(hasValue, value) {
  if (settled) return;
  settle();
  const out = { t: 'done', id: evalID, has: hasValue === true };
  if (hasValue === true) out.value = value;
  if (terminating(false)) {
    sendRaw(out, true);
  } else {
    sendRaw(out, false);
  }
}

// assertLossless 复刻官方的无损 JSON 边界：函数/symbol/undefined/BigInt/非有限数/
// 循环引用/稀疏数组/异形原型/符号键/不可枚举键一律不算"能穿的完成值"。
//
// 原型判定必须是 **realm 无关**的：script 形态的完成值由 vm realm 创建，
// 它的 Object.prototype 与外层的不是同一个对象（官方 README 的 "Script values
// can cross realms" 正是这件事）。官方的判据是"原型自己的原型是 null，且它是
// 原生 Object 构造器"（「workflow-ptc/src/realm.ts」 的 hasPlainPrototype）。
const nativeObjectSource = Function.prototype.toString.call(Object);
function isIntrinsicObjectConstructor(ctor) {
  if (typeof ctor !== 'function') return false;
  try { return Function.prototype.toString.call(ctor) === nativeObjectSource; }
  catch (e) { return false; }
}

function isPlainObjectPrototype(proto) {
  if (proto === null) return true;
  if (typeof proto !== 'object') return false;
  if (Object.getPrototypeOf(proto) !== null) return false;
  const descriptor = Object.getOwnPropertyDescriptor(proto, 'constructor');
  if (descriptor === undefined) return false;
  return isIntrinsicObjectConstructor(descriptor.value);
}

function assertNoSymbolKeys(value, pathStr) {
  if (Object.getOwnPropertySymbols(value).length > 0) {
    throw new Error(pathStr + ': symbol-keyed properties are not JSON data');
  }
}

function objectDataKeys(value, pathStr) {
  assertNoSymbolKeys(value, pathStr);
  const keys = Object.keys(value);
  // 不可枚举的自有键会被 JSON.stringify 静默丢掉 —— 那就不是无损。
  if (Reflect.ownKeys(value).length !== keys.length) {
    throw new Error(pathStr + ': non-enumerable own properties are not JSON data');
  }
  return keys;
}

function assertDenseArray(value, pathStr) {
  assertNoSymbolKeys(value, pathStr);
  // 密集数组的自有键 = 每个下标 + length；多一个就是数组上的非索引属性，少一个就是空洞。
  if (Reflect.ownKeys(value).length !== value.length + 1) {
    throw new Error(pathStr + ': arrays with non-index properties or holes are not JSON data');
  }
}

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
    assertDenseArray(value, pathStr);
    for (let i = 0; i < value.length; i += 1) {
      if (!(i in value)) throw new Error(pathStr + '[' + i + ']: sparse array hole is not a JSON value');
      assertLossless(value[i], pathStr + '[' + i + ']', seen);
    }
    seen.delete(value);
    return;
  }
  if (!isPlainObjectPrototype(Object.getPrototypeOf(value))) {
    const proto = Object.getPrototypeOf(value);
    const name = proto && proto.constructor && proto.constructor.name ? proto.constructor.name : 'unknown';
    throw new Error(pathStr + ': exotic prototype ' + name + ' is not a JSON object');
  }
  const keys = objectDataKeys(value, pathStr);
  for (let i = 0; i < keys.length; i += 1) {
    assertLossless(value[keys[i]], pathStr + '.' + keys[i], seen);
  }
  seen.delete(value);
}

// makeErrorClass 物化一个**真的构造器**（官方 src/bootstrap.ts:232-242 同形）：
// 被拒绝的成员调用是它的实例，因此程序里 e instanceof ToolCallError 成立。
// 早先这里是"工厂函数返回普通 Error"——名字对了但 instanceof 永远是 false，
// 那是把契约做成了空壳。
function makeErrorClass(spec) {
  class BindingError extends Error {
    constructor(member, message) {
      super(message);
      Object.defineProperty(this, 'name', {
        value: spec.name, enumerable: true, writable: true, configurable: true,
      });
      Object.defineProperty(this, spec.memberNameProperty, {
        value: member, enumerable: true, writable: true, configurable: true,
      });
    }
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

function makeNamespace(ns, errClass) {
  const obj = Object.create(null); // 空原型：__proto__ / constructor 只是普通自有属性
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

function buildFunctionBody(program, globals) {
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

// createScriptContext 复刻官方 evaluators/vm-script.ts:32-42：
// 空原型 globals → createContext → 在 context 里跑 'globalThis' 拿 realm →
// 把外层 globalThis 的自有描述符原样拷进来（跳过 global 与 realm 已有的键）→
// 补 globals.global = realm。
//
// 注意 realm 自带一个 console，但它**写不到任何地方**（Node 的 vm context 里
// console 是空气），所以下面显式用捕获用的 shim 覆盖它——官方
// 「src/process.ts:115」 是同一件事。
function createScriptContext() {
  const globals = Object.create(null);
  const context = vm.createContext(globals);
  const realm = new vm.Script('globalThis').runInContext(context);
  const keys = Reflect.ownKeys(globalThis);
  for (let i = 0; i < keys.length; i += 1) {
    const key = keys[i];
    if (key === 'global') continue;
    if (Object.prototype.hasOwnProperty.call(realm, key)) continue;
    Object.defineProperty(globals, key, Object.getOwnPropertyDescriptor(globalThis, key));
  }
  globals.global = realm;
  return context;
}

const TOP_LEVEL_AWAIT_HINT = 'bare top-level await needs the official Acorn-based lowering ' +
  '(dsh-ptc-runtime-node src/evaluators/vm-script-await.ts:193 processTopLevelAwait), which this ' +
  'provider does not port. Put the awaiting code in an async function and make its call the final ' +
  'expression: (async () => { ... })(). The runtime awaits a final expression that is a Promise.';

function scriptOptions(filename) {
  const options = { filename: filename };
  // 官方 vm-script.ts:55 用的是 USE_MAIN_CONTEXT_DEFAULT_LOADER（走本进程的加载器）。
  if (vm.constants && vm.constants.USE_MAIN_CONTEXT_DEFAULT_LOADER !== undefined) {
    options.importModuleDynamically = vm.constants.USE_MAIN_CONTEXT_DEFAULT_LOADER;
  }
  return options;
}

function installScriptGlobals(context, namespaces) {
  context.console = consoleShim;
  for (let i = 0; i < namespaces.length; i += 1) {
    const ns = namespaces[i];
    const errClass = ns.errorClass ? makeErrorClass(ns.errorClass) : null;
    context[ns.global] = makeNamespace(ns, errClass);
    // 官方 src/process.ts:129-134：错误构造器同时也是程序可 instanceof 的全局。
    if (ns.errorClass) context[ns.errorClass.name] = errClass;
  }
}

function compileScript(code, filename) {
  return new vm.Script(code, scriptOptions(filename));
}

async function runScript(f) {
  if (scriptContext === null) scriptContext = createScriptContext();
  const context = scriptContext;
  installScriptGlobals(context, f.namespaces || []);
  const filename = nodePath.resolve(process.cwd(), 'ptc-program.js');
  let compiled;
  try {
    compiled = compileScript(f.program, filename);
  } catch (error) {
    const message = describeError(error);
    if (error instanceof SyntaxError && String(error.message).indexOf('await is only valid in async functions') !== -1) {
      fail('exception', 'program did not compile: ' + message + '\n' + TOP_LEVEL_AWAIT_HINT);
      return;
    }
    fail('exception', 'program did not compile: ' + message);
    return;
  }
  let value;
  try {
    // 与官方 vm-script.ts:58 同形：runInContext 的完成值如果是 Promise 就等它。
    value = await compiled.runInContext(context);
  } catch (error) {
    fail('exception', describeError(error));
    return;
  }
  if (value === undefined) { done(false); return; }
  try {
    assertLossless(value, 'result', new Set());
  } catch (error) {
    fail('invalid-output', describeError(error));
    return;
  }
  done(true, value);
}

async function runFunctionBody(f) {
  const namespaces = f.namespaces || [];
  const globals = ['console'];
  const values = [consoleShim];
  for (let i = 0; i < namespaces.length; i += 1) {
    globals.push(namespaces[i].global);
    values.push(makeNamespace(namespaces[i], namespaces[i].errorClass ? makeErrorClass(namespaces[i].errorClass) : null));
  }
  let fn;
  try {
    fn = buildFunctionBody(f.program, globals);
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
  if (value === undefined) { done(false); return; }
  try {
    assertLossless(value, 'result', new Set());
  } catch (e) {
    fail('invalid-output', describeError(e));
    return;
  }
  done(true, value);
}

function beginEval(f) {
  evalID = f.id || 1;
  maxOutput = f.maxOutputBytes || 0;
  outBytes = 0;
  settled = false;
  activeEvaluation = { running: true };
  programFormat = f.format === 'script' ? 'script' : 'function-body';
  stateRetained = f.retainsState === true;
  if (f.cwd) {
    try { process.chdir(f.cwd); } catch (e) { /* 宿主已校验过 cwd，这里失败不致命 */ }
  }
}

process.on('uncaughtException', function (e) {
  // 官方 executionInstructions 原文："an uncaught callback exception or unhandled
  // rejection terminates the process" —— 所以这里结算之后必须退出。
  fail('exception', 'uncaught: ' + describeError(e), true);
});
process.on('unhandledRejection', function (e) {
  fail('exception', 'unhandled rejection: ' + describeError(e), true);
});

clearProgramEnvironment();

const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
rl.on('line', function (line) {
  const text = line.trim();
  if (!text) return;
  let f;
  try {
    f = JSON.parse(text);
  } catch (e) {
    fail('protocol', 'host frame is not valid JSON: ' + describeError(e), true);
    return;
  }
  if (f.t === 'bindResult') { onBindResult(f); return; }
  if (f.t === 'eval') {
    if (evalCount > 0 && !stateRetained) { fail('protocol', 'second eval on a per-call worker', true); return; }
    if (evalCount > 0 && (f.retainsState === true) !== stateRetained) {
      fail('protocol', 'eval changed its state-retention mode on a live worker', true);
      return;
    }
    evalCount += 1;
    beginEval(f);
    const current = activeEvaluation;
    evaluationContext.run(current, function () {
      const run = programFormat === 'script' ? runScript(f) : runFunctionBody(f);
      run.catch(function (e) { fail('exception', 'worker internal failure: ' + describeError(e), true); });
    });
    return;
  }
  if (f.t === 'cancel') {
    // 单线程里打断不了同步循环：宿主会按 stopGrace 直接终止进程树。
    return;
  }
  fail('protocol', 'unknown host frame ' + String(f.t), true);
});

rl.on('close', function () {
  if (!settled) {
    settled = true;
    process.exit(0);
  }
});

sendRaw({ t: 'ready', pid: process.pid, version: process.version });
`
