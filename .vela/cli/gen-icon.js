#!/usr/bin/env node
// Generates a 32x32 PNG icon for cmd/winclient/assets/icon.png
// Uses only Node.js built-ins (zlib, fs)

const zlib = require('zlib');
const fs = require('fs');
const path = require('path');

const WIDTH = 32;
const HEIGHT = 32;

function u32be(n) {
  const b = Buffer.alloc(4);
  b.writeUInt32BE(n, 0);
  return b;
}

function writeChunk(type, data) {
  const typeBuffer = Buffer.from(type, 'ascii');
  const crcInput = Buffer.concat([typeBuffer, data]);
  const crc = crc32(crcInput);
  return Buffer.concat([u32be(data.length), typeBuffer, data, u32be(crc)]);
}

// CRC32 table
const CRC_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) {
      c = (c & 1) ? (0xEDB88320 ^ (c >>> 1)) : (c >>> 1);
    }
    table[n] = c;
  }
  return table;
})();

function crc32(buf) {
  let crc = 0xFFFFFFFF;
  for (const byte of buf) {
    crc = CRC_TABLE[(crc ^ byte) & 0xFF] ^ (crc >>> 8);
  }
  return (crc ^ 0xFFFFFFFF) >>> 0;
}

// Generate pixel data
const rows = [];
const cx = 16, cy = 16, radius = 10;
for (let y = 0; y < HEIGHT; y++) {
  const row = [0]; // filter byte = None
  for (let x = 0; x < WIDTH; x++) {
    const dx = x - cx, dy = y - cy;
    const inCircle = Math.sqrt(dx * dx + dy * dy) <= radius;
    if (inCircle) {
      row.push(255, 255, 255); // white
    } else {
      row.push(0x1a, 0x73, 0xe8); // blue #1a73e8
    }
  }
  rows.push(Buffer.from(row));
}

const rawData = Buffer.concat(rows);
const compressed = zlib.deflateRawSync(rawData, { level: 9 });

// Proper zlib wrapper: CMF + FLG + deflate data + Adler32
function adler32(buf) {
  let s1 = 1, s2 = 0;
  for (const b of buf) {
    s1 = (s1 + b) % 65521;
    s2 = (s2 + s1) % 65521;
  }
  return ((s2 << 16) | s1) >>> 0;
}

const cmf = 0x78; // deflate, window size 32K
const flg = 0x9C; // default compression, check bits
const a32 = adler32(rawData);
const a32be = Buffer.alloc(4);
a32be.writeUInt32BE(a32, 0);
const idatData = Buffer.concat([Buffer.from([cmf, flg]), compressed, a32be]);

// Build PNG
const sig = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);

const ihdrData = Buffer.alloc(13);
ihdrData.writeUInt32BE(WIDTH, 0);
ihdrData.writeUInt32BE(HEIGHT, 4);
ihdrData[8] = 8;  // bit depth
ihdrData[9] = 2;  // color type: RGB
ihdrData[10] = 0; // compression
ihdrData[11] = 0; // filter
ihdrData[12] = 0; // interlace

const png = Buffer.concat([
  sig,
  writeChunk('IHDR', ihdrData),
  writeChunk('IDAT', idatData),
  writeChunk('IEND', Buffer.alloc(0)),
]);

const outPath = path.join(__dirname, '../../cmd/winclient/assets/icon.png');
fs.mkdirSync(path.dirname(outPath), { recursive: true });
fs.writeFileSync(outPath, png);
console.log(`Icon written: ${outPath} (${png.length} bytes)`);
