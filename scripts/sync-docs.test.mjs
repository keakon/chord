import assert from 'node:assert/strict';
import test from 'node:test';
import { rewriteAdmonitions, rewriteLinks } from './sync-docs.mjs';

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

for (const [name, input, lang, expected] of [
  ['bare sibling', '[Page](usage.md)', 'en', '[Page](/chord/usage/)'],
  ['relative sibling', '[Page](./usage.md#keys)', 'en', '[Page](/chord/usage/#keys)'],
  ['Chinese sibling', '[Page](usage_CN.md#keys)', 'zh', '[Page](/chord/zh/usage/#keys)'],
  ['English link from Chinese', '[Page](usage.md)', 'zh', '[Page](/chord/zh/usage/)'],
  ['external URL', '[Page](https://example.invalid/usage.md)', 'en', '[Page](https://example.invalid/usage.md)'],
  ['parent sibling', '[Page](../usage.md)', 'en', '[Page](/chord/usage/)'],
  ['ordinary parentheses', 'See (model-configs.md).', 'en', 'See (model-configs.md).'],
  ['inline code', '`[Page](usage.md)` and [Page](usage.md)', 'en', '`[Page](usage.md)` and [Page](/chord/usage/)'],
  ['long inline delimiter', '`` `[Page](usage.md)` ``', 'en', '`` `[Page](usage.md)` ``'],
  ['fenced code', '```md\n[Page](usage.md)\n```\n[Page](usage.md)', 'en', '```md\n[Page](usage.md)\n```\n[Page](/chord/usage/)'],
  ['tilde fence', '~~~md\n[Page](usage.md)\n~~~', 'en', '~~~md\n[Page](usage.md)\n~~~'],
  ['nested fence', '````md\n```\n[Page](usage.md)\n```\n````', 'en', '````md\n```\n[Page](usage.md)\n```\n````'],
  ['unclosed fence', '```md\n[Page](usage.md)', 'en', '```md\n[Page](usage.md)'],
  ['indented code', '    [Page](usage.md)', 'en', '    [Page](usage.md)'],
  ['quoted fence', '> ```md\n> [Page](usage.md)\n> ```', 'en', '> ```md\n> [Page](usage.md)\n> ```'],
  ['escaped link', '\\[Page](usage.md)', 'en', '\\[Page](usage.md)'],
  ['unmatched bracket', 'Example ](usage.md)', 'en', 'Example ](usage.md)'],
  ['image link', '![Image](usage.md)', 'en', '![Image](/chord/usage/)'],
]) {
  test(name, () => assert.equal(rewriteLinks(input, lang), expected));
}
