import { spawnSync } from 'node:child_process'
import { globSync, readFileSync, writeFileSync } from 'node:fs'

const protoFiles = globSync('contracts/**/*.proto', {
  exclude: ['contracts/gen/**'],
})

const games = [
  'liars-dice',
  'dice-789',
  'meet-by-chance',
]

if (protoFiles.length === 0) {
  console.log('SKIPPED: no production Proto files')
  process.exit(0)
}

const platformResult = spawnSync('buf', ['generate', 'contracts'], { stdio: 'inherit' })
if (platformResult.error) {
  throw platformResult.error
}
if (platformResult.status !== 0) {
  process.exit(platformResult.status ?? 1)
}

for (const game of games) {
  const result = spawnSync(
    'buf',
    ['generate', `games/${game}/proto`, '--template', `games/${game}/buf.gen.yaml`],
    { stdio: 'inherit' },
  )
  if (result.error) {
    throw result.error
  }
  if (result.status !== 0) {
    process.exit(result.status ?? 1)
  }
}

// protoc-gen-es currently emits an extra blank line at EOF; normalize it so generated
// files remain stable across platforms and pass the repository's whitespace checks.
const generatedTypeScriptFiles = [
  ...globSync('contracts/gen/ts/platform/game/**/*.ts'),
  ...games.flatMap((game) => globSync(`games/${game}/client/src/generated/**/*.ts`)),
]
for (const file of generatedTypeScriptFiles) {
  const normalized = readFileSync(file, 'utf8').replace(/\s+$/u, '\n')
  writeFileSync(file, normalized)
}
