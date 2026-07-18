import { spawnSync } from 'node:child_process'

const generatedPaths = [
  'contracts/gen',
  'platform/persistence/postgres/sqlcgen',
]

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    encoding: 'utf8',
    stdio: options.capture ? 'pipe' : 'inherit',
  })
  if (result.error) {
    throw result.error
  }
  return result
}

const packageManagerScript = process.env.npm_execpath
if (!packageManagerScript) {
  throw new Error('check:generated must run through pnpm')
}
const generate = run(process.execPath, [packageManagerScript, 'run', 'generate'])
if (generate.status !== 0) {
  process.exit(generate.status ?? 1)
}

const trackedDiff = run('git', ['diff', '--exit-code', '--', ...generatedPaths])
if (trackedDiff.status !== 0) {
  console.error('generated files differ from the tracked baseline')
  process.exit(trackedDiff.status ?? 1)
}

const untracked = run(
  'git',
  ['ls-files', '--others', '--exclude-standard', '--', ...generatedPaths],
  { capture: true },
)
if (untracked.status !== 0) {
  process.stderr.write(untracked.stderr)
  process.exit(untracked.status ?? 1)
}
if (untracked.stdout.trim() !== '') {
  console.error(`untracked generated files:\n${untracked.stdout.trim()}`)
  process.exit(1)
}

console.log('generated files match the tracked baseline')
