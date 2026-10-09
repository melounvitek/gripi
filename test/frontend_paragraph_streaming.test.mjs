// The first three tests are ported from T3 Code, https://github.com/pingdotgg/t3code
// apps/server/src/orchestration-v2/assistantStreaming.test.ts at commit 33806e73555107b8ac8fb18fd15b9dfb87ec6557
//
// MIT License
//
// Copyright (c) 2026 T3 Tools Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
import assert from "node:assert/strict";
import { test } from "node:test";

import { finishedMarkdownBlocks } from "../public/assets/paragraph_streaming.js";

test("delivers completed paragraphs", () => {
  assert.equal(finishedMarkdownBlocks("First"), "");
  assert.equal(finishedMarkdownBlocks("First\n\nSec"), "First\n\n");
  assert.equal(finishedMarkdownBlocks("First\n\nSecond\n\nThi"), "First\n\nSecond\n\n");
  assert.equal(finishedMarkdownBlocks("First\n\nSecond\n\nThird"), "First\n\nSecond\n\n");
});

test("keeps code fences intact", () => {
  assert.equal(finishedMarkdownBlocks("Intro\n\n```ts\nx()\n\n"), "Intro\n\n");
  assert.equal(finishedMarkdownBlocks("```ts\nx()\n```\nrest"), "```ts\nx()\n```\n");
});

test("holds a streamed section heading until its content has a boundary", () => {
  assert.equal(finishedMarkdownBlocks("Intro\n\n## Results\n\n"), "Intro\n\n");
  assert.equal(finishedMarkdownBlocks("**Results**\n\nBody\n\nNext"), "**Results**\n\nBody\n\n");
});

test("delivers a tight list one item at a time", () => {
  assert.equal(finishedMarkdownBlocks("- one\n- tw"), "- one\n");
  assert.equal(finishedMarkdownBlocks("- one\n- two\n- th"), "- one\n- two\n");
  assert.equal(finishedMarkdownBlocks("1. one\n2. tw"), "1. one\n");
});

test("does not count a list item until its marker is followed by text", () => {
  for (const text of ["- one\n-", "- one\n- ", "1. one\n2.", "1. one\n2. "]) {
    assert.equal(finishedMarkdownBlocks(text), "", JSON.stringify(text));
  }
});

test("ends a block at an unindented ATX heading even without a blank line", () => {
  assert.equal(finishedMarkdownBlocks("Intro\n## Results\nBo"), "Intro\n");
  assert.equal(finishedMarkdownBlocks("Intro\n ## Results\nBo"), "");
});

test("closes a code fence only with a bare fence of the same kind and at least the same length", () => {
  assert.equal(finishedMarkdownBlocks("```\ncode\n```js\nmore\n"), "");
  assert.equal(finishedMarkdownBlocks("```\ncode\n~~~\nmore\n"), "");
  assert.equal(finishedMarkdownBlocks("```\ncode\n````\nrest"), "```\ncode\n````\n");
});

test("does not end a block at a blank line inside a code fence", () => {
  assert.equal(finishedMarkdownBlocks("```\na\n\nb\n\n"), "");
});

test("ignores carriage returns and trailing whitespace on boundary lines", () => {
  assert.equal(finishedMarkdownBlocks("First\r\n\r\nSec"), "First\r\n\r\n");
  assert.equal(finishedMarkdownBlocks("First  \n \t\nSec"), "First  \n \t\n");
  assert.equal(finishedMarkdownBlocks("```\r\nx\r\n```  \r\nrest"), "```\r\nx\r\n```  \r\n");
});

test("does not treat a line with a no-break space as blank", () => {
  assert.equal(finishedMarkdownBlocks("First\n\u00a0\nSec"), "");
});

test("keeps the delivered items when a tight list later becomes loose", () => {
  assert.equal(finishedMarkdownBlocks("- one\n- tw"), "- one\n");
  assert.equal(finishedMarkdownBlocks("- one\n- two\n\n- thr"), "- one\n- two\n\n");
});
