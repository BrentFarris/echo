const qrVersions = [
  { version: 1, blocks: [{ count: 1, data: 19, ecc: 7 }] },
  { version: 2, blocks: [{ count: 1, data: 34, ecc: 10 }] },
  { version: 3, blocks: [{ count: 1, data: 55, ecc: 15 }] },
  { version: 4, blocks: [{ count: 1, data: 80, ecc: 20 }] },
  { version: 5, blocks: [{ count: 1, data: 108, ecc: 26 }] },
  { version: 6, blocks: [{ count: 2, data: 68, ecc: 18 }] },
];

type QRVersion = (typeof qrVersions)[number];

export function renderQRCodeSVG(text: string): string {
  const bytes = Array.from(new TextEncoder().encode(text));
  const spec = qrVersions.find((item) => bytes.length + 2 <= totalDataCodewords(item));
  if (!spec) {
    return `<p class="qr-error">URL is too long for the built-in QR encoder.</p>`;
  }

  const data = encodeData(bytes, totalDataCodewords(spec));
  const codewords = interleaveBlocks(data, spec);
  const size = spec.version * 4 + 17;
  const matrix = Array.from({ length: size }, () => Array(size).fill(false));
  const reserved = Array.from({ length: size }, () => Array(size).fill(false));

  drawFunctionPatterns(matrix, reserved, spec.version);
  drawFormatBits(matrix, reserved, 0);
  drawCodewords(matrix, reserved, codewords, 0);

  const quiet = 4;
  const viewSize = size + quiet * 2;
  const paths: string[] = [];
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      if (matrix[y][x]) {
        paths.push(`M${x + quiet},${y + quiet}h1v1h-1z`);
      }
    }
  }

  return `
    <svg class="qr-code" viewBox="0 0 ${viewSize} ${viewSize}" role="img" aria-label="Web access QR code">
      <rect width="${viewSize}" height="${viewSize}" fill="#fff"/>
      <path d="${paths.join("")}" fill="#000"/>
    </svg>
  `;
}

function totalDataCodewords(spec: QRVersion): number {
  return spec.blocks.reduce((total, block) => total + block.count * block.data, 0);
}

function encodeData(bytes: number[], dataCodewords: number): number[] {
  const bits: number[] = [];
  appendBits(bits, 0x4, 4);
  appendBits(bits, bytes.length, 8);
  for (const byte of bytes) {
    appendBits(bits, byte, 8);
  }
  const capacityBits = dataCodewords * 8;
  appendBits(bits, 0, Math.min(4, capacityBits - bits.length));
  while (bits.length % 8 !== 0) {
    bits.push(0);
  }

  const output: number[] = [];
  for (let i = 0; i < bits.length; i += 8) {
    output.push(bits.slice(i, i + 8).reduce((value, bit) => (value << 1) | bit, 0));
  }
  for (let pad = 0; output.length < dataCodewords; pad++) {
    output.push(pad % 2 === 0 ? 0xec : 0x11);
  }
  return output;
}

function appendBits(bits: number[], value: number, length: number) {
  for (let i = length - 1; i >= 0; i--) {
    bits.push((value >>> i) & 1);
  }
}

function interleaveBlocks(data: number[], spec: QRVersion): number[] {
  const blocks: { data: number[]; ecc: number[] }[] = [];
  let offset = 0;
  for (const group of spec.blocks) {
    for (let i = 0; i < group.count; i++) {
      const blockData = data.slice(offset, offset + group.data);
      offset += group.data;
      blocks.push({
        data: blockData,
        ecc: reedSolomonRemainder(blockData, group.ecc),
      });
    }
  }

  const output: number[] = [];
  const maxData = Math.max(...blocks.map((block) => block.data.length));
  const maxEcc = Math.max(...blocks.map((block) => block.ecc.length));
  for (let i = 0; i < maxData; i++) {
    for (const block of blocks) {
      if (i < block.data.length) {
        output.push(block.data[i]);
      }
    }
  }
  for (let i = 0; i < maxEcc; i++) {
    for (const block of blocks) {
      output.push(block.ecc[i]);
    }
  }
  return output;
}

function reedSolomonRemainder(data: number[], degree: number): number[] {
  const divisor = reedSolomonDivisor(degree);
  const result = Array(degree).fill(0);
  for (const byte of data) {
    const factor = byte ^ result.shift()!;
    result.push(0);
    for (let i = 0; i < divisor.length; i++) {
      result[i] ^= gfMultiply(divisor[i], factor);
    }
  }
  return result;
}

function reedSolomonDivisor(degree: number): number[] {
  const result = Array(degree).fill(0);
  result[degree - 1] = 1;
  let root = 1;
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < result.length; j++) {
      result[j] = gfMultiply(result[j], root);
      if (j + 1 < result.length) {
        result[j] ^= result[j + 1];
      }
    }
    root = gfMultiply(root, 0x02);
  }
  return result;
}

function gfMultiply(x: number, y: number): number {
  let z = 0;
  for (let i = 7; i >= 0; i--) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d);
    z ^= ((y >>> i) & 1) * x;
  }
  return z;
}

function drawFunctionPatterns(matrix: boolean[][], reserved: boolean[][], version: number) {
  const size = matrix.length;
  drawFinder(matrix, reserved, 3, 3);
  drawFinder(matrix, reserved, size - 4, 3);
  drawFinder(matrix, reserved, 3, size - 4);
  for (let i = 8; i < size - 8; i++) {
    setFunction(matrix, reserved, 6, i, i % 2 === 0);
    setFunction(matrix, reserved, i, 6, i % 2 === 0);
  }
  for (const y of alignmentPositions(version)) {
    for (const x of alignmentPositions(version)) {
      if ((x === 6 && y === 6) || (x === size - 7 && y === 6) || (x === 6 && y === size - 7)) {
        continue;
      }
      drawAlignment(matrix, reserved, x, y);
    }
  }
}

function drawFinder(matrix: boolean[][], reserved: boolean[][], cx: number, cy: number) {
  for (let dy = -4; dy <= 4; dy++) {
    for (let dx = -4; dx <= 4; dx++) {
      const x = cx + dx;
      const y = cy + dy;
      if (!inBounds(matrix, x, y)) {
        continue;
      }
      const dist = Math.max(Math.abs(dx), Math.abs(dy));
      setFunction(matrix, reserved, x, y, dist !== 2 && dist !== 4);
    }
  }
}

function drawAlignment(matrix: boolean[][], reserved: boolean[][], cx: number, cy: number) {
  for (let dy = -2; dy <= 2; dy++) {
    for (let dx = -2; dx <= 2; dx++) {
      setFunction(matrix, reserved, cx + dx, cy + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
    }
  }
}

function drawFormatBits(matrix: boolean[][], reserved: boolean[][], mask: number) {
  const size = matrix.length;
  const bits = formatBits((1 << 3) | mask);
  for (let i = 0; i <= 5; i++) {
    setFunction(matrix, reserved, 8, i, bit(bits, i));
  }
  setFunction(matrix, reserved, 8, 7, bit(bits, 6));
  setFunction(matrix, reserved, 8, 8, bit(bits, 7));
  setFunction(matrix, reserved, 7, 8, bit(bits, 8));
  for (let i = 9; i < 15; i++) {
    setFunction(matrix, reserved, 14 - i, 8, bit(bits, i));
  }
  for (let i = 0; i < 8; i++) {
    setFunction(matrix, reserved, size - 1 - i, 8, bit(bits, i));
  }
  for (let i = 8; i < 15; i++) {
    setFunction(matrix, reserved, 8, size - 15 + i, bit(bits, i));
  }
  setFunction(matrix, reserved, 8, size - 8, true);
}

function formatBits(format: number): number {
  let data = format << 10;
  for (let i = 14; i >= 10; i--) {
    if (((data >>> i) & 1) !== 0) {
      data ^= 0x537 << (i - 10);
    }
  }
  return ((format << 10) | data) ^ 0x5412;
}

function drawCodewords(matrix: boolean[][], reserved: boolean[][], codewords: number[], mask: number) {
  const size = matrix.length;
  let bitIndex = 0;
  let upward = true;
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) {
      right--;
    }
    for (let vert = 0; vert < size; vert++) {
      const y = upward ? size - 1 - vert : vert;
      for (let dx = 0; dx < 2; dx++) {
        const x = right - dx;
        if (reserved[y][x]) {
          continue;
        }
        const byte = codewords[Math.floor(bitIndex / 8)] ?? 0;
        const value = ((byte >>> (7 - (bitIndex % 8))) & 1) !== 0;
        matrix[y][x] = value !== maskBit(mask, x, y);
        bitIndex++;
      }
    }
    upward = !upward;
  }
}

function maskBit(mask: number, x: number, y: number): boolean {
  switch (mask) {
    case 0:
      return (x + y) % 2 === 0;
    default:
      return false;
  }
}

function alignmentPositions(version: number): number[] {
  switch (version) {
    case 1:
      return [];
    case 2:
      return [6, 18];
    case 3:
      return [6, 22];
    case 4:
      return [6, 26];
    case 5:
      return [6, 30];
    case 6:
      return [6, 34];
    default:
      return [];
  }
}

function setFunction(matrix: boolean[][], reserved: boolean[][], x: number, y: number, value: boolean) {
  if (!inBounds(matrix, x, y)) {
    return;
  }
  matrix[y][x] = value;
  reserved[y][x] = true;
}

function inBounds(matrix: boolean[][], x: number, y: number): boolean {
  return y >= 0 && y < matrix.length && x >= 0 && x < matrix.length;
}

function bit(value: number, index: number): boolean {
  return ((value >>> index) & 1) !== 0;
}
