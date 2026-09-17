import assert from "node:assert/strict";
import { test } from "node:test";
import { appendQuote } from "../public/assets/conversation_controller.js";

test("quotes selected lines and leaves a blank line for the reply", () => {
  assert.equal(appendQuote("", "First line\n\n  indented code"), "> First line\n> \n>   indented code\n\n");
  assert.equal(appendQuote("", "First\r\nSecond"), "> First\n> Second\n\n");
});

test("appends quotes without removing or replacing any existing draft text", () => {
  for (const draft of ["Existing draft…", "  unfinished draft  ", "Existing draft\n", "Existing draft\n\n"]) {
    const result = appendQuote(draft, "Selected passage");
    assert.ok(result.startsWith(draft));
    assert.equal(result, `${draft}${draft.endsWith("\n\n") ? "" : draft.endsWith("\n") ? "\n" : "\n\n"}> Selected passage\n\n`);
  }
});

test("supports quoting several passages into the same draft", () => {
  assert.equal(appendQuote(appendQuote("My question", "First"), "Second"), "My question\n\n> First\n\n> Second\n\n");
});
