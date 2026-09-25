import fs from 'node:fs';

const src = JSON.parse(
  fs.readFileSync(new URL('./liquid-vue.json', import.meta.url), 'utf8'),
).files[0].content;

// print everything after the last big shader block: the Vue component definition
const k = src.indexOf('/* ------------------------------------------------------------------');
const k2 = src.indexOf('createRectCache(');
console.log('=== usages of rectCache ===');
for (const m of src.matchAll(/.{0,160}rectCache.{0,200}/gs)) {
  console.log(m[0].replace(/\s+/g, ' '));
}
console.log('=== tail (vue component) ===');
console.log(src.slice(-6000));