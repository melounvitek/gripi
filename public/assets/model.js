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

export function scopedPickerModels(models, scopedModels) {
  const available = new Map(models.map((model) => [modelSettingsKey(model), model]));
  return scopedModels.map((scoped) => available.get(modelSettingsKey(scoped))).filter(Boolean);
}

// Pi CLI's /model order: the current model first, then by provider.
export function sortedPickerModels(models, currentModel) {
  const currentKey = modelSettingsKey(currentModel || {});
  return [...models].sort((a, b) =>
    (modelSettingsKey(b) === currentKey) - (modelSettingsKey(a) === currentKey)
      || String(a.provider || "").localeCompare(String(b.provider || "")));
}

export function matchingPickerModels(models, search) {
  const words = search.toLowerCase().split(/\s+/).filter(Boolean);
  return models.filter((model) => {
    const text = `${model.provider}/${model.id} ${model.name}`.toLowerCase();
    return words.every((word) => text.includes(word));
  });
}
