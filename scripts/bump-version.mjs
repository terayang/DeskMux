#!/usr/bin/env node
/**
 * Bumps the app version in the three places that must stay in sync
 * (docs/RELEASE.md): root package.json, frontend/package.json, and
 * wails.json info.productVersion.
 *
 * Usage: node scripts/bump-version.mjs [major|minor|patch]   (default: patch)
 *
 * Wired as a `pre` hook of the dist:* packaging scripts so every local
 * package build automatically carries a fresh patch version and never
 * overwrites a same-named artifact again. CI release builds invoke wails
 * directly and are unaffected (the released version comes from the tag).
 */
import { readFileSync, writeFileSync } from 'node:fs'

const kind = process.argv[2] ?? 'patch'
if (!['major', 'minor', 'patch'].includes(kind)) {
  console.error(`usage: node scripts/bump-version.mjs [major|minor|patch]`)
  process.exit(2)
}

const root = JSON.parse(readFileSync('package.json', 'utf8'))
const [major, minor, patch] = root.version.split('.').map(Number)
const next =
  kind === 'major'
    ? `${major + 1}.0.0`
    : kind === 'minor'
      ? `${major}.${minor + 1}.0`
      : `${major}.${minor}.${patch + 1}`

const writeJson = (path, mutate) => {
  const data = JSON.parse(readFileSync(path, 'utf8'))
  mutate(data)
  writeFileSync(path, JSON.stringify(data, null, 2) + '\n')
}

writeJson('package.json', (d) => (d.version = next))
writeJson('frontend/package.json', (d) => (d.version = next))
writeJson('wails.json', (d) => (d.info.productVersion = next))

console.log(`version: ${root.version} -> ${next}`)
