const DEFAULT_MAX_INPUT_CHARS = 262_144;
const DEFAULT_MAX_COLUMNS = 500;
const DEFAULT_MAX_ROWS = 200;
const DEFAULT_SCROLLBACK = 2_000;
const MIN_COLUMNS = 80;
const MIN_ROWS = 24;
const FULL_SCREEN_CLEAR = /\x1b\[2J(?:\x1b\[(?:H|1;1H))?/g;

let terminalModulePromise;

export function hasTerminalControls(text) {
  const value = String(text || "");
  return /[\x08\x1b\u0080-\u009f]/.test(value) || /\r(?!\n)/.test(value);
}

export async function renderTerminalOutput(text, options = {}) {
  const source = String(text || "");
  if (!hasTerminalControls(source)) return null;

  const maxInputChars = positiveInteger(options.maxInputChars, DEFAULT_MAX_INPUT_CHARS);
  const maxColumns = positiveInteger(options.maxColumns, DEFAULT_MAX_COLUMNS);
  const maxRows = positiveInteger(options.maxRows, DEFAULT_MAX_ROWS);
  const { input, truncated } = boundedInput(source, maxInputChars);
  const frame = latestFullScreenFrame(input);
  const columns = Math.min(maxColumns, Math.max(1, Math.max(MIN_COLUMNS, measuredColumns(frame))));
  const rows = Math.min(maxRows, Math.max(1, Math.max(MIN_ROWS, measuredRows(frame))));
  const { Terminal } = await terminalModule();
  const terminal = new Terminal({
    allowProposedApi: false,
    cols: columns,
    rows,
    scrollback: Math.min(DEFAULT_SCROLLBACK, maxRows * 10),
    convertEol: true,
    disableStdin: true
  });

  try {
    await writeTerminal(terminal, input);
    return { lines: terminalLines(terminal), columns, rows, truncated };
  } finally {
    terminal.dispose();
  }
}

function terminalModule() {
  terminalModulePromise ||= import("./vendor/xterm/xterm.mjs");
  return terminalModulePromise;
}

function positiveInteger(value, fallback) {
  const number = Number(value);
  return Number.isInteger(number) && number > 0 ? number : fallback;
}

function boundedInput(source, maximum) {
  if (source.length <= maximum) return { input: source, truncated: false };

  let latestClear = -1;
  for (const match of source.matchAll(FULL_SCREEN_CLEAR)) latestClear = match.index;
  const latestFrame = latestClear >= 0 ? source.slice(latestClear) : "";
  return {
    input: latestFrame && latestFrame.length <= maximum ? latestFrame : source.slice(-maximum),
    truncated: true
  };
}

function latestFullScreenFrame(source) {
  let start = 0;
  for (const match of source.matchAll(FULL_SCREEN_CLEAR)) start = match.index + match[0].length;
  return source.slice(start);
}

function measuredColumns(source) {
  return source
    .replace(/\x1b\][\s\S]*?(?:\x07|\x1b\\)/g, "")
    .replace(/\x1bP[\s\S]*?\x1b\\/g, "")
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "")
    .split(/[\r\n]/)
    .reduce((maximum, line) => Math.max(maximum, [...line].length), 0);
}

function measuredRows(source) {
  return Math.max(source.split(/\r?\n/).length, 1);
}

function writeTerminal(terminal, input) {
  return new Promise((resolve) => terminal.write(input, resolve));
}

function terminalLines(terminal) {
  const buffer = terminal.buffer.active;
  const lines = [];
  for (let row = 0; row < buffer.length; row += 1) {
    const line = buffer.getLine(row);
    if (line) lines.push(terminalLine(buffer, line));
  }
  while (lines.length > 1 && lines[lines.length - 1].text === "") lines.pop();
  return lines;
}

function terminalLine(buffer, line) {
  const cells = [];
  const reusableCell = buffer.getNullCell();
  let lastVisibleIndex = -1;

  for (let column = 0; column < line.length; column += 1) {
    const cell = line.getCell(column, reusableCell);
    if (!cell || cell.getWidth() === 0) continue;

    const style = cellStyle(cell);
    const characters = cell.isInvisible() ? " " : (cell.getChars() || " ");
    cells.push({ text: characters, style });
    if (characters !== " " || style.background) lastVisibleIndex = cells.length - 1;
  }

  const visibleCells = cells.slice(0, lastVisibleIndex + 1);
  const runs = [];
  visibleCells.forEach((cell) => {
    const key = JSON.stringify(cell.style);
    const previous = runs[runs.length - 1];
    if (previous?.key === key) previous.text += cell.text;
    else runs.push({ text: cell.text, style: cell.style, key });
  });
  runs.forEach((run) => delete run.key);
  return { text: runs.map((run) => run.text).join(""), runs };
}

function cellStyle(cell) {
  const style = {};
  if (!cell.isFgDefault()) style.foreground = cellColor(cell, "Fg");
  if (!cell.isBgDefault()) style.background = cellColor(cell, "Bg");
  if (cell.isBold()) style.bold = true;
  if (cell.isDim()) style.dim = true;
  if (cell.isItalic()) style.italic = true;
  if (cell.isUnderline()) style.underline = true;
  if (cell.isInverse()) style.inverse = true;
  if (cell.isStrikethrough()) style.strikethrough = true;
  if (cell.isOverline()) style.overline = true;
  return style;
}

function cellColor(cell, channel) {
  return {
    mode: cell[`is${channel}RGB`]() ? "rgb" : "palette",
    value: cell[`get${channel}Color`]()
  };
}
