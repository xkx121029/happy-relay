import fs from 'node:fs';

const j = JSON.parse(fs.readFileSync(new URL('./liquid-vue.json', import.meta.url), 'utf8'));
console.log('name', j.name);
console.log('dependencies', JSON.stringify(j.dependencies));
console.log('registryDependencies', JSON.stringify(j.registryDependencies));
for (const f of j.files) console.log('FILE', f.path, f.content.length);

const src = j.files[0].content;
const imports = [...src.matchAll(/from\s+"([^"]+)"/g)].map((m) => m[1]);
console.log('imports', JSON.stringify([...new Set(imports)]));

const i = src.indexOf('uHasContent');
console.log('--- uHasContent context ---');
console.log(src.slice(Math.max(0, i - 500), i + 500));

const k = src.indexOf('defineComponent');
console.log('--- vue wrapper section ---');
console.log(src.slice(Math.max(0, k - 200), k + 2600));