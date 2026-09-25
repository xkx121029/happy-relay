import fs from 'node:fs';

const reg = JSON.parse(
  fs.readFileSync(new URL('./registry.json', import.meta.url), 'utf8'),
);
const items = Array.isArray(reg) ? reg : (reg.items ?? []);
console.log('total items', items.length);
const names = items.map((i) => i.name);
console.log('rect-cache like:', names.filter((n) => /rect/i.test(n)));
console.log('vue items:', names.filter((n) => /vue/.test(n)).slice(0, 60));

const hit = items.find((i) => /rect/i.test(i.name));
if (hit) {
  console.log('--- hit ---', hit.name, hit.type);
  fs.writeFileSync(
    new URL('./rect-cache.json', import.meta.url),
    JSON.stringify(hit, null, 2),
  );
  for (const f of hit.files ?? []) console.log('FILE', f.path, f.content.length);
}

const liquid = items.find((i) => i.name === 'liquid-vue');
console.log('liquid registryDependencies:', JSON.stringify(liquid?.registryDependencies));
console.log('liquid has uHasContent lines:');
const c = liquid?.files?.[0]?.content ?? '';
for (const m of c.matchAll(/.{0,120}uHasContent.{0,120}/gs)) {
  console.log('   ', m[0].replace(/\s+/g, ' '));
}