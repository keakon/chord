import assert from 'node:assert/strict';
import test from 'node:test';
import { rewriteAdmonitions } from './sync-docs.mjs';

for (const [name, input, expected] of [
  ['custom title', '> **Details:** Example text.', ':::note[Details]\nExample text.\n:::'],
  ['Chinese title', '> **提示：** 示例文本。', ':::note[提示]\n示例文本。\n:::'],
  ['warning', '> **Warning:** Example text.', ':::caution[Warning]\nExample text.\n:::'],
  ['multiline', '> **Note:** First line.\n> Second line.', ':::note[Note]\nFirst line.\nSecond line.\n:::'],
  ['ordinary quote', '> Ordinary text.', '> Ordinary text.'],
]) {
  test(name, () => {
    assert.equal(rewriteAdmonitions(input), expected);
  });
}
