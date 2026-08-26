// A QR code for a link, drawn as inline SVG. A share address is short, so a byte
// mode encoder covering versions 1 to 10 is enough to carry one, and keeping the
// encoder here means no extra dependency and a code that still appears when the
// machine is offline.
// Developed by X Project.

import clsx from 'clsx'
import { useMemo } from 'react'

export type QRLevel = 'L' | 'M' | 'Q' | 'H'

export interface QRCodeProps {
  value: string
  /** size is the drawn width and height in pixels, quiet zone included. */
  size?: number
  className?: string
  /** quiet is the width of the empty border, counted in modules. */
  quiet?: number
}

/**
 * QRCode draws value as a scannable code. Dark modules are painted in the
 * current text colour and the quiet zone is left transparent, so the caller
 * decides the two shades. A value too long to encode is shown as plain text
 * instead of a code that would not scan.
 */
export function QRCode({ value, size = 176, className, quiet = 4 }: QRCodeProps) {
  const matrix = useMemo(() => qrMatrix(value), [value])
  const margin = Math.max(0, Math.round(quiet))

  if (matrix.length === 0) {
    return (
      <div
        className={clsx('flex items-center justify-center break-all px-3 py-3 font-mono text-[11px]', className)}
        style={{ width: size, minHeight: size }}
      >
        {value}
      </div>
    )
  }

  const span = matrix.length + margin * 2
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox={`0 0 ${span} ${span}`}
      className={className}
      shapeRendering="crispEdges"
      role="img"
      aria-label="QR code for this link"
    >
      <path d={qrPath(matrix, margin)} fill="currentColor" />
    </svg>
  )
}

/** qrPath merges each run of dark modules into one shape so the SVG stays small. */
function qrPath(matrix: boolean[][], margin: number): string {
  const parts: string[] = []
  for (let y = 0; y < matrix.length; y++) {
    const row = matrix[y]
    let x = 0
    while (x < row.length) {
      if (!row[x]) {
        x += 1
        continue
      }
      let run = 1
      while (x + run < row.length && row[x + run]) run += 1
      parts.push(`M${x + margin} ${y + margin}h${run}v1h-${run}z`)
      x += run
    }
  }
  return parts.join('')
}

// ---- the encoder -------------------------------------------------------------

/** QR_TOTAL_CODEWORDS is the codeword count, data and correction together. */
const QR_TOTAL_CODEWORDS = [26, 44, 70, 100, 134, 172, 196, 242, 292, 346]

/** QR_EC_PER_BLOCK is how many correction codewords each block carries. */
const QR_EC_PER_BLOCK: Record<QRLevel, number[]> = {
  L: [7, 10, 15, 20, 26, 18, 20, 24, 30, 18],
  M: [10, 16, 26, 18, 24, 16, 18, 22, 22, 26],
  Q: [13, 22, 18, 26, 18, 24, 18, 22, 20, 24],
  H: [17, 28, 22, 16, 22, 28, 26, 26, 24, 28],
}

/** QR_BLOCK_COUNT is how many blocks the data is split into. */
const QR_BLOCK_COUNT: Record<QRLevel, number[]> = {
  L: [1, 1, 1, 1, 1, 2, 2, 2, 2, 4],
  M: [1, 1, 1, 2, 2, 4, 4, 4, 5, 5],
  Q: [1, 1, 2, 2, 4, 4, 6, 6, 8, 8],
  H: [1, 1, 2, 4, 4, 4, 5, 6, 8, 8],
}

/** QR_ALIGNMENT lists the row and column centres of the alignment patterns. */
const QR_ALIGNMENT: number[][] = [
  [],
  [6, 18],
  [6, 22],
  [6, 26],
  [6, 30],
  [6, 34],
  [6, 22, 38],
  [6, 24, 42],
  [6, 26, 46],
  [6, 28, 50],
]

/** QR_LEVEL_BITS is the two bit code each correction level is written with. */
const QR_LEVEL_BITS: Record<QRLevel, number> = { L: 1, M: 0, Q: 3, H: 2 }

/** QR_FINDER_RUN is the run of shades a finder pattern makes on a scan line. */
const QR_FINDER_RUN = [true, false, true, true, true, false, true, false, false, false, false]

/**
 * qrMatrix encodes value and returns the modules row by row, where true is a
 * dark module. Byte mode is used and the smallest version from 1 to 10 that
 * holds the value is picked. A value that does not fit, or an empty one,
 * returns an empty matrix rather than throwing, so the caller can show
 * something else.
 */
export function qrMatrix(value: string, level: QRLevel = 'M'): boolean[][] {
  if (!value) return []
  const bytes = qrBytes(value)
  const version = qrPickVersion(bytes.length, level)
  if (version === 0) return []
  return qrDraw(version, level, qrCodewords(bytes, version, level))
}

/** qrBytes turns the text into the UTF-8 octets byte mode carries. */
function qrBytes(value: string): number[] {
  return Array.from(new TextEncoder().encode(value))
}

/** qrPickVersion returns the smallest version that fits, or zero for none. */
function qrPickVersion(length: number, level: QRLevel): number {
  for (let version = 1; version <= 10; version++) {
    const needed = 4 + qrCountBits(version) + length * 8
    if (needed <= qrDataCodewords(version, level) * 8) return version
  }
  return 0
}

/** qrCountBits is the width of the character count that follows the mode. */
function qrCountBits(version: number): number {
  return version < 10 ? 8 : 16
}

/** qrDataCodewords is what is left for data once correction is taken out. */
function qrDataCodewords(version: number, level: QRLevel): number {
  const ec = QR_EC_PER_BLOCK[level][version - 1] * QR_BLOCK_COUNT[level][version - 1]
  return QR_TOTAL_CODEWORDS[version - 1] - ec
}

/**
 * qrCodewords builds the bit stream, pads it out to the version, then returns
 * the data and correction codewords in the interleaved order they are placed in.
 */
function qrCodewords(bytes: number[], version: number, level: QRLevel): number[] {
  const total = qrDataCodewords(version, level)
  const capacity = total * 8
  const bits: number[] = []
  const push = (value: number, width: number) => {
    for (let i = width - 1; i >= 0; i--) bits.push((value >> i) & 1)
  }

  push(4, 4)
  push(bytes.length, qrCountBits(version))
  for (const byte of bytes) push(byte, 8)

  // The terminator is up to four zeroes, then the stream is filled to whole
  // codewords with the two pad bytes the standard names.
  for (let i = 0; i < 4 && bits.length < capacity; i++) bits.push(0)
  while (bits.length % 8 !== 0) bits.push(0)

  const data: number[] = []
  for (let i = 0; i < bits.length; i += 8) {
    let byte = 0
    for (let j = 0; j < 8; j++) byte = (byte << 1) | bits[i + j]
    data.push(byte)
  }
  let pad = 0xec
  while (data.length < total) {
    data.push(pad)
    pad = pad === 0xec ? 0x11 : 0xec
  }

  return qrInterleave(data, version, level)
}

/**
 * qrInterleave splits the data into blocks, adds the correction codewords of
 * each block, then reads the blocks column by column the way a reader expects.
 */
function qrInterleave(data: number[], version: number, level: QRLevel): number[] {
  const ecLength = QR_EC_PER_BLOCK[level][version - 1]
  const count = QR_BLOCK_COUNT[level][version - 1]
  const shortLength = Math.floor(data.length / count)
  const longBlocks = data.length % count
  const generator = qrGenerator(ecLength)

  const dataBlocks: number[][] = []
  const ecBlocks: number[][] = []
  let offset = 0
  for (let i = 0; i < count; i++) {
    const length = shortLength + (i >= count - longBlocks ? 1 : 0)
    const block = data.slice(offset, offset + length)
    offset += length
    dataBlocks.push(block)
    ecBlocks.push(qrRemainder(block, generator))
  }

  const out: number[] = []
  for (let i = 0; i <= shortLength; i++) {
    for (const block of dataBlocks) {
      if (i < block.length) out.push(block[i])
    }
  }
  for (let i = 0; i < ecLength; i++) {
    for (const block of ecBlocks) out.push(block[i])
  }
  return out
}

/** qrField builds the exponent and log tables of GF(256) with 0x11d. */
function qrField(): { exp: Uint8Array; log: Uint8Array } {
  const exp = new Uint8Array(512)
  const log = new Uint8Array(256)
  let x = 1
  for (let i = 0; i < 255; i++) {
    exp[i] = x
    log[x] = i
    x <<= 1
    if (x & 0x100) x ^= 0x11d
  }
  for (let i = 255; i < 512; i++) exp[i] = exp[i - 255]
  return { exp, log }
}

const QR_FIELD = qrField()

/** qrMul multiplies two field elements. */
function qrMul(a: number, b: number): number {
  if (a === 0 || b === 0) return 0
  return QR_FIELD.exp[QR_FIELD.log[a] + QR_FIELD.log[b]]
}

/** qrGenerator returns the generator polynomial of the given degree. */
function qrGenerator(degree: number): number[] {
  let poly = [1]
  for (let i = 0; i < degree; i++) {
    const next = new Array<number>(poly.length + 1).fill(0)
    for (let j = 0; j < poly.length; j++) {
      next[j] ^= poly[j]
      next[j + 1] ^= qrMul(poly[j], QR_FIELD.exp[i])
    }
    poly = next
  }
  return poly
}

/** qrRemainder is the Reed Solomon remainder, which is the block correction. */
function qrRemainder(block: number[], generator: number[]): number[] {
  const result = new Array<number>(generator.length - 1).fill(0)
  for (const byte of block) {
    const factor = byte ^ result[0]
    result.shift()
    result.push(0)
    for (let i = 0; i < result.length; i++) result[i] ^= qrMul(generator[i + 1], factor)
  }
  return result
}

// ---- the grid ----------------------------------------------------------------

/**
 * qrDraw lays the function patterns down, threads the codewords through the
 * free modules, then picks the mask that scores best under the four rules.
 */
function qrDraw(version: number, level: QRLevel, codewords: number[]): boolean[][] {
  const size = version * 4 + 17
  const modules = qrGrid(size)
  const reserved = qrGrid(size)

  qrDrawPatterns(modules, reserved, version, level)
  qrDrawData(modules, reserved, codewords)

  let best = 0
  let bestScore = Number.POSITIVE_INFINITY
  for (let mask = 0; mask < 8; mask++) {
    qrApplyMask(modules, reserved, mask)
    qrDrawFormat(modules, reserved, level, mask)
    const score = qrPenalty(modules)
    if (score < bestScore) {
      bestScore = score
      best = mask
    }
    qrApplyMask(modules, reserved, mask)
  }
  qrApplyMask(modules, reserved, best)
  qrDrawFormat(modules, reserved, level, best)
  return modules
}

/** qrGrid makes a square of light modules. */
function qrGrid(size: number): boolean[][] {
  const grid: boolean[][] = []
  for (let y = 0; y < size; y++) grid.push(new Array<boolean>(size).fill(false))
  return grid
}

/** qrSet writes one module and marks it as belonging to a pattern. */
function qrSet(modules: boolean[][], reserved: boolean[][], x: number, y: number, dark: boolean): void {
  modules[y][x] = dark
  reserved[y][x] = true
}

/** qrDrawPatterns draws everything that is fixed by the version and level. */
function qrDrawPatterns(modules: boolean[][], reserved: boolean[][], version: number, level: QRLevel): void {
  const size = modules.length

  // The timing patterns run the whole way across, then the finder patterns and
  // their separators are drawn over the ends of them.
  for (let i = 0; i < size; i++) {
    qrSet(modules, reserved, 6, i, i % 2 === 0)
    qrSet(modules, reserved, i, 6, i % 2 === 0)
  }

  qrDrawFinder(modules, reserved, 0, 0)
  qrDrawFinder(modules, reserved, size - 7, 0)
  qrDrawFinder(modules, reserved, 0, size - 7)

  // An alignment pattern is skipped wherever a finder pattern already sits.
  const centres = QR_ALIGNMENT[version - 1]
  for (const cy of centres) {
    for (const cx of centres) {
      const corner = (cx === 6 && cy === 6) || (cx === 6 && cy === size - 7) || (cx === size - 7 && cy === 6)
      if (corner) continue
      qrDrawAlignment(modules, reserved, cx, cy)
    }
  }

  qrDrawVersion(modules, reserved, version)
  // The real format bits depend on the mask, this only holds the space for them.
  qrDrawFormat(modules, reserved, level, 0)
}

/** qrDrawFinder draws one finder pattern together with its separator. */
function qrDrawFinder(modules: boolean[][], reserved: boolean[][], left: number, top: number): void {
  const size = modules.length
  for (let dy = -1; dy <= 7; dy++) {
    for (let dx = -1; dx <= 7; dx++) {
      const x = left + dx
      const y = top + dy
      if (x < 0 || y < 0 || x >= size || y >= size) continue
      const ring = Math.max(Math.abs(dx - 3), Math.abs(dy - 3))
      qrSet(modules, reserved, x, y, ring <= 3 && ring !== 2)
    }
  }
}

/** qrDrawAlignment draws the five by five pattern centred on cx and cy. */
function qrDrawAlignment(modules: boolean[][], reserved: boolean[][], cx: number, cy: number): void {
  for (let dy = -2; dy <= 2; dy++) {
    for (let dx = -2; dx <= 2; dx++) {
      qrSet(modules, reserved, cx + dx, cy + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1)
    }
  }
}

/** qrDrawVersion writes the version block, which versions 7 and up carry. */
function qrDrawVersion(modules: boolean[][], reserved: boolean[][], version: number): void {
  if (version < 7) return
  const size = modules.length
  let rem = version
  for (let i = 0; i < 12; i++) rem = (rem << 1) ^ (((rem >> 11) & 1) * 0x1f25)
  const bits = (version << 12) | rem
  for (let i = 0; i < 18; i++) {
    const dark = ((bits >> i) & 1) === 1
    const far = size - 11 + (i % 3)
    const near = Math.floor(i / 3)
    qrSet(modules, reserved, far, near, dark)
    qrSet(modules, reserved, near, far, dark)
  }
}

/** qrDrawFormat writes both copies of the level and mask, with their BCH code. */
function qrDrawFormat(modules: boolean[][], reserved: boolean[][], level: QRLevel, mask: number): void {
  const size = modules.length
  const data = (QR_LEVEL_BITS[level] << 3) | mask
  let rem = data
  for (let i = 0; i < 10; i++) rem = (rem << 1) ^ (((rem >> 9) & 1) * 0x537)
  const bits = ((data << 10) | rem) ^ 0x5412
  const bit = (index: number) => ((bits >> index) & 1) === 1

  for (let i = 0; i <= 5; i++) qrSet(modules, reserved, 8, i, bit(i))
  qrSet(modules, reserved, 8, 7, bit(6))
  qrSet(modules, reserved, 8, 8, bit(7))
  qrSet(modules, reserved, 7, 8, bit(8))
  for (let i = 9; i < 15; i++) qrSet(modules, reserved, 14 - i, 8, bit(i))

  for (let i = 0; i < 8; i++) qrSet(modules, reserved, size - 1 - i, 8, bit(i))
  for (let i = 8; i < 15; i++) qrSet(modules, reserved, 8, size - 15 + i, bit(i))
  qrSet(modules, reserved, 8, size - 8, true)
}

/** qrDrawData walks the two module wide zigzag and lays the codewords in it. */
function qrDrawData(modules: boolean[][], reserved: boolean[][], codewords: number[]): void {
  const size = modules.length
  const total = codewords.length * 8
  let index = 0
  for (let right = size - 1; right >= 1; right -= 2) {
    // Column six is the vertical timing pattern, so the pairs step around it.
    if (right === 6) right = 5
    for (let step = 0; step < size; step++) {
      for (let column = 0; column < 2; column++) {
        const x = right - column
        const upward = ((right + 1) & 2) === 0
        const y = upward ? size - 1 - step : step
        if (reserved[y][x] || index >= total) continue
        modules[y][x] = ((codewords[index >> 3] >> (7 - (index & 7))) & 1) === 1
        index += 1
      }
    }
  }
}

/** qrApplyMask flips the free modules the mask covers. Applying it twice undoes it. */
function qrApplyMask(modules: boolean[][], reserved: boolean[][], mask: number): void {
  const size = modules.length
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      if (reserved[y][x]) continue
      if (qrMaskCovers(mask, x, y)) modules[y][x] = !modules[y][x]
    }
  }
}

/** qrMaskCovers is the condition of the eight standard masks. */
function qrMaskCovers(mask: number, x: number, y: number): boolean {
  switch (mask) {
    case 0:
      return (x + y) % 2 === 0
    case 1:
      return y % 2 === 0
    case 2:
      return x % 3 === 0
    case 3:
      return (x + y) % 3 === 0
    case 4:
      return (Math.floor(y / 2) + Math.floor(x / 3)) % 2 === 0
    case 5:
      return ((x * y) % 2) + ((x * y) % 3) === 0
    case 6:
      return (((x * y) % 2) + ((x * y) % 3)) % 2 === 0
    default:
      return (((x + y) % 2) + ((x * y) % 3)) % 2 === 0
  }
}

/** qrPenalty scores a masked grid under the four rules, where lower is better. */
function qrPenalty(modules: boolean[][]): number {
  const size = modules.length
  let score = 0
  let dark = 0

  for (let y = 0; y < size; y++) {
    const row = modules[y]
    const column: boolean[] = []
    for (let x = 0; x < size; x++) {
      column.push(modules[x][y])
      if (row[x]) dark += 1
    }
    score += qrRunScore(row) + qrRunScore(column)
    score += qrFinderScore(row) + qrFinderScore(column)
  }

  // Two by two blocks of one shade.
  for (let y = 0; y < size - 1; y++) {
    for (let x = 0; x < size - 1; x++) {
      const shade = modules[y][x]
      if (shade === modules[y][x + 1] && shade === modules[y + 1][x] && shade === modules[y + 1][x + 1]) score += 3
    }
  }

  // How far the balance of dark against light strays from half.
  const drift = Math.abs((dark * 100) / (size * size) - 50)
  score += Math.floor(drift / 5) * 10
  return score
}

/** qrRunScore charges for runs of five or more modules of one shade. */
function qrRunScore(line: boolean[]): number {
  let score = 0
  let run = 1
  for (let i = 1; i < line.length; i++) {
    if (line[i] === line[i - 1]) {
      run += 1
      continue
    }
    if (run >= 5) score += run - 2
    run = 1
  }
  if (run >= 5) score += run - 2
  return score
}

/** qrFinderScore charges for anything that reads like a finder pattern. */
function qrFinderScore(line: boolean[]): number {
  let score = 0
  for (let i = 0; i + 11 <= line.length; i++) {
    let forward = true
    let backward = true
    for (let j = 0; j < 11; j++) {
      if (line[i + j] !== QR_FINDER_RUN[j]) forward = false
      if (line[i + j] !== QR_FINDER_RUN[10 - j]) backward = false
    }
    if (forward || backward) score += 40
  }
  return score
}
