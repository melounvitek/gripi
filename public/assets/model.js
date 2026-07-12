import { THINKING_LEVELS } from "./constants.js";

export function supportedThinkingLevels(model) {
  if (!model?.reasoning) return ["off"];
  const map = model.thinkingLevelMap || {};
  return THINKING_LEVELS.filter((level) => {
    if (["xhigh", "max"].includes(level)) return map[level] !== undefined && map[level] !== null;
    return map[level] !== null;
  });
}

export function selectedThinkingLevel(model, currentLevel) {
  const levels = supportedThinkingLevels(model);
  if (levels.includes(currentLevel)) return currentLevel;
  const currentIndex = THINKING_LEVELS.indexOf(currentLevel);
  const higher = levels.find((level) => THINKING_LEVELS.indexOf(level) >= currentIndex);
  if (higher) return higher;
  const lower = levels.filter((level) => THINKING_LEVELS.indexOf(level) < currentIndex);
  return lower[lower.length - 1] || levels[0] || "off";
}

export function modelSettingsKey(model) {
  return `${model.provider || ""}\u0000${model.id || ""}`;
}
