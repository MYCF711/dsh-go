// verify-node-reads.mjs —— 用 Node 自己的 zstd（DSH 用的就是 node:zlib）读 Go 写出来的会话帧。
//
// 这是 P0 判据③「Node 能读 Go 写的帧」的最小可执行证据：
//   node scripts/verify-node-reads.mjs <file.v4.jsonl.zstd>
//
// 它做的事和 DSH 的 assertZstdHeaderFrame 完全一致：
//   1. 按帧结构切分（不算 zstd 内容，只看帧头与块头）；
//   2. 每帧用 zlib.zstdDecompressSync 解；
//   3. 首帧必须恰好一行且以 \n 结尾。
import fs from 'node:fs';
import zlib from 'node:zlib';

const MAGIC = 0xfd2fb528;

function splitFrames(buf) {
  const out = [];
  let pos = 0;
  while (pos < buf.length) {
    const start = pos;
    if (buf.length - pos < 5) throw new Error(`帧头截断 @${pos}`);
    if (buf.readUInt32LE(pos) !== MAGIC) throw new Error(`@${pos} 不是 zstd 魔数`);
    pos += 4;
    const fhd = buf[pos++];
    const single = (fhd & 0x20) !== 0;
    const checksum = (fhd & 0x04) !== 0;
    const dictFlag = fhd & 0x03;
    const fcsFlag = (fhd >> 6) & 0x03;
    if (!single) pos += 1;
    pos += [0, 1, 2, 4][dictFlag];
    const fcsSize = fcsFlag === 0 ? (single ? 1 : 0) : [0, 2, 4, 8][fcsFlag];
    pos += fcsSize;
    for (;;) {
      const bh = buf[pos] | (buf[pos + 1] << 8) | (buf[pos + 2] << 16);
      pos += 3;
      const last = (bh & 1) === 1;
      const type = (bh >> 1) & 0x03;
      const size = bh >>> 3;
      if (type === 0 || type === 2) pos += size;
      else if (type === 1) pos += 1;
      else throw new Error(`保留块类型 @${pos - 3}`);
      if (last) break;
    }
    if (checksum) pos += 4;
    out.push(buf.subarray(start, pos));
  }
  return out;
}

const file = process.argv[2];
if (!file) {
  console.error('用法: node scripts/verify-node-reads.mjs <session.v4.jsonl.zstd>');
  process.exit(2);
}
const buf = fs.readFileSync(file);
const frames = splitFrames(buf);

let lines = 0;
frames.forEach((frame, i) => {
  const plain = zlib.zstdDecompressSync(frame);
  const nl = plain.filter((b) => b === 10).length;
  if (i === 0) {
    if (plain.length === 0 || plain.indexOf(10) !== plain.length - 1) {
      throw new Error('首帧不是恰好一行 header');
    }
    const header = JSON.parse(plain.toString('utf8').trimEnd());
    if (header.type !== 'session' || header.version !== 4) {
      throw new Error(`header 不合法: ${plain}`);
    }
    const allowed = new Set(['type', 'version', 'id', 'createdAt', 'isSeeded', 'delegationDepth', 'cwd', 'parentSession', 'origin', 'agentPreset']);
    const extra = Object.keys(header).filter((k) => !allowed.has(k));
    if (extra.length) throw new Error(`header 有白名单外的键: ${extra}`);
    console.log(`header OK: id=${header.id} keys=${Object.keys(header).length}`);
  }
  lines += nl;
});
console.log(`Node 读取成功：${frames.length} 帧 / ${lines} 行 / ${buf.length} 字节`);
