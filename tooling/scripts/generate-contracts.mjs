import { spawnSync } from 'node:child_process'
import { globSync } from 'node:fs'

const protoFiles = globSync('contracts/**/*.proto', {
  exclude: ['contracts/gen/**'],
})

if (protoFiles.length === 0) {
  console.log('SKIPPED: no production Proto files')
  process.exit(0)
}

const result = spawnSync('buf', ['generate'], { stdio: 'inherit' })
if (result.error) {
  throw result.error
}
process.exit(result.status ?? 1)
