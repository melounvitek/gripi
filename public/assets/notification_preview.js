(function installNotificationPreview(scope) {
  function protectFencedNotificationCode(source, protect) {
    const lines = source.split("\n");
    const output = [];
    for (let index = 0; index < lines.length; index += 1) {
      const opening = lines[index].match(/^\s{0,3}(`{3,}|~{3,})(.*)$/);
      if (!opening || (opening[1][0] === "`" && opening[2].includes("`"))) {
        output.push(lines[index]);
        continue;
      }

      let closingIndex = index + 1;
      while (closingIndex < lines.length) {
        const closing = lines[closingIndex].match(/^\s{0,3}(`{3,}|~{3,})[ \t]*$/);
        if (closing && closing[1][0] === opening[1][0] && closing[1].length >= opening[1].length) break;
        closingIndex += 1;
      }

      output.push(protect(lines.slice(index + 1, closingIndex).join("\n")));
      index = closingIndex;
    }
    return output.join("\n");
  }

  function protectInlineNotificationCode(source, protect) {
    const runs = [];
    for (let index = 0; index < source.length; index += 1) {
      if (source[index] !== "`") continue;

      let end = index + 1;
      while (source[end] === "`") end += 1;
      runs.push({ start: index, end, length: end - index });
      index = end - 1;
    }

    const nextMatchingRun = new Array(runs.length);
    const nextByLength = new Map();
    for (let index = runs.length - 1; index >= 0; index -= 1) {
      nextMatchingRun[index] = nextByLength.get(runs[index].length);
      nextByLength.set(runs[index].length, index);
    }

    let output = "";
    let cursor = 0;
    for (let index = 0; index < runs.length; index += 1) {
      if (runs[index].start < cursor || nextMatchingRun[index] == null) continue;

      const closing = runs[nextMatchingRun[index]];
      output += source.slice(cursor, runs[index].start) + protect(source.slice(runs[index].end, closing.start));
      cursor = closing.end;
      index = nextMatchingRun[index];
    }
    return output + source.slice(cursor);
  }

  function stripNotificationEmphasis(source) {
    const nestedPatterns = [
      /(^|[\s([{>"'])\*\*(?=\S)([^*\n]*?)\*([^*\n]+)\*\*\*(?=$|[\s)\]}>,.!?;:"'])/gm,
      /(^|[\s([{>"'])\*(?=\S)([^*\n]*?)\*\*([^*\n]+)\*\*\*(?=$|[\s)\]}>,.!?;:"'])/gm,
    ];
    const patterns = [
      /(^|[\s([{>"'])\*\*\*(?=\S)([^*\n]*?\S)\*\*\*(?=$|[\s)\]}>,.!?;:"'])/gm,
      /(^|[\s([{>"'])___(?=\S)([^_\n]*?\S)___(?=$|[\s)\]}>,.!?;:"'])/gm,
      /(^|[\s([{>"'])~~(?=\S)([^~\n]*?\S)~~(?=$|[\s)\]}>,.!?;:"'])/gm,
      /(^|[\s([{>"'])\*\*(?=\S)([^*\n]*?\S)\*\*(?=$|[\s)\]}>,.!?;:"'])/gm,
      /(^|[\s([{>"'])__(?=\S)([^_\n]*?\S)__(?=$|[\s)\]}>,.!?;:"'])/gm,
      /(^|[\s([{>"'])\*(?=\S)([^*\n]*?\S)\*(?=$|[\s)\]}>,.!?;:"'])/gm,
      /(^|[\s([{>"'])_(?=\S)([^_\n]*?\S)_(?=$|[\s)\]}>,.!?;:"'])/gm,
    ];

    for (let pass = 0; pass < 3; pass += 1) {
      const previous = source;
      nestedPatterns.forEach((pattern) => { source = source.replace(pattern, "$1$2$3"); });
      patterns.forEach((pattern) => { source = source.replace(pattern, "$1$2"); });
      if (source === previous) break;
    }
    return source;
  }

  scope.gripiNotificationReplyPreview = function notificationReplyPreview(text) {
    const source = String(text || "").replace(/\r\n?/g, "\n");
    const protectedSegments = [];
    const sourceCharacters = new Set(source);
    let markerCodePoint = 0xE000;
    while (markerCodePoint <= 0x10FFFF && sourceCharacters.has(String.fromCodePoint(markerCodePoint))) markerCodePoint += 1;
    let segmentMarker = markerCodePoint <= 0x10FFFF ? String.fromCodePoint(markerCodePoint) : "\uE000\uE001";
    while (source.includes(segmentMarker)) segmentMarker += "\uE001";
    const protect = (value) => `${segmentMarker}${protectedSegments.push(value) - 1}${segmentMarker}`;

    let preview = protectFencedNotificationCode(source, protect);
    preview = protectInlineNotificationCode(preview, protect)
      .replace(/!\[([^\]]*)\]\([^)]+\)/g, "$1")
      .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
      .replace(/<(https?:\/\/[^>\s]+)>/gi, (_match, url) => protect(url))
      .replace(/\bhttps?:\/\/[^\s<]+/gi, (url) => protect(url))
      .replace(/^\s{0,3}>\s?/gm, "")
      .replace(/^\s{0,3}#{1,6}\s+/gm, "")
      .replace(/^\s*(?:[-*+] |\d+[.)]\s+)/gm, "")
      .replace(/^\s*[-*_]{3,}\s*$/gm, " ")
      .replace(/<\/?[a-z][^>]*>/gi, " ")
      .replace(/\bjavascript:/gi, "");
    preview = stripNotificationEmphasis(preview);
    const protectedSegmentPattern = new RegExp(`${segmentMarker}(\\d+)${segmentMarker}`, "g");
    for (let pass = 0; pass <= protectedSegments.length; pass += 1) {
      const restored = preview.replace(protectedSegmentPattern, (_match, index) => protectedSegments[Number(index)]);
      if (restored === preview) break;
      preview = restored;
    }
    preview = preview.replace(/\s+/g, " ").trim();
    if (!preview) return "New reply.";

    const characters = Array.from(preview);
    return characters.length > 180 ? `${characters.slice(0, 177).join("")}…` : preview;
  };
})(globalThis);
