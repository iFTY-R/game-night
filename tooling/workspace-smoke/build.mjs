import { copyFile, mkdir, rm } from 'node:fs/promises'

const outputDirectory = new URL('./dist/', import.meta.url)

// Fully replacing the output directory ensures Turbo caches only artifacts produced from current source.
await rm(outputDirectory, { recursive: true, force: true })
await mkdir(outputDirectory, { recursive: true })
await copyFile(new URL('./src/add.mjs', import.meta.url), new URL('./index.mjs', outputDirectory))
