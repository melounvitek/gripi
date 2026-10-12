// Speech cleaning and sentence splitting, ported from Hermes Desktop's
// speech-text.ts (MIT): Markdown, code, links and identifiers become text a
// voice can read, or silence.

const MIN_SENTENCE_LENGTH = 24;
const SENTENCE_END_RE = /[.!?…。！？]+["'”’)\]]*\s+/g;
const EMOJI_RE = /(?:[\u{1F000}-\u{1FAFF}\u{2600}-\u{27BF}]|[\u{FE0F}\u{200D}]|[\u{E0020}-\u{E007F}])+/gu;
const THINKING_PREFIX_RE = /^\s*(?:\([^)\n]{1,48}\)\s*)?(?:processing|thinking|reasoning|analyzing|pondering|contemplating|musing|cogitating|ruminating|deliberating|mulling|reflecting|computing|synthesizing|formulating|brainstorming)\.\.\.\s*/i;
// Hermes's file tokens ("MEDIA:/path/report.xlsx"); punctuation after them stays.
const MEDIA_PATH_RE = /[ \t]*MEDIA:\S+?(?=[.,;:!?)\]]*(?:\s|$))/g;
const TABLE_DELIMITER_CELL_RE = /^:?-{3,}:?$/;
const FILENAME_RE = /[\w.-]{0,60}\.(?:wav|ogg|mp3|flac|m4a|aac|py|pyc|ts|tsx|js|jsx|mjs|cjs|json|yaml|yml|toml|md|mdx|txt|csv|xlsx|xls|pdf|png|jpg|jpeg|gif|webp|svg|log|sql|sh|bash|zsh|rs|go|java|rb|php|html|css|lock|tar|gz|zip|db|sqlite|sqlite3|onnx|pt|bin|env|ini|conf|cfg|xml)\b/i;
const UUID_RE = /[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/;
const HASH_RE = /\b(?:sha(?:-?256|-?512|-?1|3)?|blake2[ab]?|md5|crc32?)[:\s]+[0-9a-fA-F]{7,64}/gi;
const HEX_RUN_RE = /[0-9a-fA-F]{7,64}/;
const IDENTIFIER_RE = /[A-Za-z0-9_./~@-]+/g;
const DATE_RE = /^\d{4}[-/]\d{1,2}(?:[-/]\d{1,2})?$/;

export function speechSentences(text) {
  const speech = speechText(text);
  const sentences = [];
  let start = 0;
  for (const match of speech.matchAll(SENTENCE_END_RE)) {
    const end = match.index + match[0].length;
    const sentence = speech.slice(start, end).trim();
    if (sentence.length >= MIN_SENTENCE_LENGTH) {
      sentences.push(sentence);
      start = end;
    }
  }
  const rest = speech.slice(start).trim();
  if (rest) sentences.push(rest);
  return sentences;
}

function speechText(text) {
  const lines = summarizeTables(text.replace(/\r\n?/g, "\n"))
    // A colon ending a line introduces something (a list, code) and must not leave the voice hanging.
    .replace(/:\s*$/gm, ".")
    .replace(/^#{1,6}[ \t]+/gm, "")
    .replace(/^[ \t]*[-+*][ \t]+/gm, "");
  const flat = lines
    .replace(/(\p{L})-\n(\p{L})/gu, "$1$2")
    .replace(/([.!?])([*_~`>"'’”)}\]]*)[ \t]*\n{2,}[ \t]*/g, "$1$2 ")
    .replace(/[ \t]*\n{2,}[ \t]*/g, ". ")
    .replace(/[ \t]*\n[ \t]*/g, " ");
  const plain = flat
    .replace(/```[\s\S]*?(?:```|$)/g, "")
    .replace(THINKING_PREFIX_RE, " ")
    .replace(/\[([^\]]+)\]\(([^)]+)\)/g, "$1")
    .replace(/`([^`]+)`/g, "$1")
    .replace(/\bhttps?:\/\/\S+/gi, "")
    .replace(MEDIA_PATH_RE, "")
    .replace(EMOJI_RE, " ");
  return plain
    .replace(HASH_RE, " ")
    .replace(IDENTIFIER_RE, (token) => (isDenseIdentifier(token) ? " " : token))
    .replace(/[*_~>#]/g, "")
    .replace(/:\s*$/, ".")
    .replace(/\s+/g, " ")
    .trim();
}

// A table is announced by its header row instead of being read cell by cell.
function summarizeTables(text) {
  const lines = text.split("\n");
  const spoken = [];
  for (let index = 0; index < lines.length; index += 1) {
    const header = tableCells(lines[index]);
    const delimiter = tableCells(lines[index + 1] ?? "");
    const table = header && delimiter && header.length === delimiter.length && delimiter.every((cell) => TABLE_DELIMITER_CELL_RE.test(cell));
    if (!table) {
      spoken.push(lines[index]);
      continue;
    }
    const names = header.filter(Boolean).join(", ");
    if (names) spoken.push(/[.!?:]$/.test(names) ? names : `${names}.`);
    index += 1;
    while (tableCells(lines[index + 1] ?? "")) index += 1;
  }
  return spoken.join("\n");
}

function tableCells(line) {
  if (!line.includes("|")) return null;
  return line.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map((cell) => cell.trim());
}

// Filenames, paths, hashes, UUIDs and version IDs are left out, because a voice would spell them character by character.
function isDenseIdentifier(token) {
  if (token.includes("@") || DATE_RE.test(token)) return false;
  if (/^(?:~\/|\.\.?\/|\/)/.test(token)) return true;
  if (token.includes("/") && (FILENAME_RE.test(token) || /\d/.test(token))) return true;
  if (FILENAME_RE.test(token) || UUID_RE.test(token)) return true;
  if (!/\d/.test(token)) return false;
  if (HEX_RUN_RE.test(token)) return true;
  const separators = ["_", ".", "/"].filter((separator) => token.includes(separator)).length;
  const hyphens = token.split("-").length - 1;
  return separators >= 2 || hyphens >= 2 || (hyphens >= 1 && separators >= 1);
}
