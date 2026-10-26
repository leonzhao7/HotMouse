#!/usr/bin/env node
/**
 * GitHub 风格约束检查器。
 *
 * 对应 github-showcase/github-style.md 的 "Forbidden Classes" / "AI-Specific Rules"，
 * 目的���让后续新增的前端代码不会悄悄偏离统一风格。
 *
 * 检查范围: src/ 下的 .ts / .tsx / .css（跳过本文件与 tokens）。
 */

import { readdirSync, readFileSync, statSync } from 'node:fs'
import { extname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = fileURLToPath(new URL('..', import.meta.url))
const SRC = join(ROOT, 'src')

/** 禁止的 Tailwind 类 (来自 github-style.md 的 Forbidden Classes)。 */
const FORBIDDEN_CLASSES = [
  { pattern: /\brounded-(2xl|3xl)\b/, reason: 'GitHub 统一使用 rounded-md (6px)' },
  { pattern: /\bbg-gradient-\S+/, reason: '禁止使用渐变背景' },
  { pattern: /\bshadow-(lg|xl|2xl)\b/, reason: '禁止使用重阴影' },
  { pattern: /\bfont-serif\b/, reason: '禁止使用衬线字体' },
  { pattern: /\bblur-\S+/, reason: 'GitHub 不使用模糊效果' },
  { pattern: /\btext-(4xl|5xl|6xl|7xl|8xl|9xl)\b/, reason: '禁止使用过大的字体' },
]

/** 允许 rounded-full 的文件/场景: 仅徽章与头像/加载指示器。 */
const ROUNDED_FULL_ALLOWED = [
  'src/components/Badge.tsx',
  'src/components/Spinner.tsx',
]

/** 语义色类以外不允许出现的具体色值工具类。 */
const RAW_COLOR_CLASS = /\b(?:bg|text|border|from|to|via|ring|fill|stroke)-\[#[0-9a-fA-F]{3,8}\]/g

/** 允许直接写十六进制色的文件: 主题定义处。 */
const RAW_HEX_ALLOWED = ['src/styles/', 'src/index.css']

function walk(dir) {
  const entries = []
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) {
      entries.push(...walk(full))
    } else if (['.ts', '.tsx', '.css'].includes(extname(full))) {
      entries.push(full)
    }
  }
  return entries
}

const problems = []

for (const file of walk(SRC)) {
  const rel = relative(ROOT, file)
  const content = readFileSync(file, 'utf8')
  const lines = content.split('\n')

  lines.forEach((line, index) => {
    const lineNo = index + 1
    const code = line.replace(/\/\/.*$/, '').replace(/\/\*.*?\*\//g, '')

    for (const { pattern, reason } of FORBIDDEN_CLASSES) {
      if (pattern.test(code)) {
        problems.push({ rel, lineNo, reason, text: line.trim() })
      }
    }

    if (/\brounded-full\b/.test(code) && !ROUNDED_FULL_ALLOWED.includes(rel)) {
      problems.push({
        rel,
        lineNo,
        reason: 'rounded-full 仅允许用于徽章、头像和加载指示器',
        text: line.trim(),
      })
    }

    for (const match of code.matchAll(RAW_COLOR_CLASS)) {
      problems.push({
        rel,
        lineNo,
        reason: '请使用语义色类 (bg-canvas / text-fg-muted / border-border 等)',
        text: match[0],
      })
    }

    const hexMatches = code.match(/#[0-9a-fA-F]{3,8}\b/g) ?? []
    if (hexMatches.length > 0 && !RAW_HEX_ALLOWED.some((prefix) => rel.startsWith(prefix))) {
      for (const hex of hexMatches) {
        problems.push({
          rel,
          lineNo,
          reason: '组件中不允许硬编码色值，请使用 CSS 变量或语义色类',
          text: hex,
        })
      }
    }
  })
}

if (problems.length > 0) {
  console.error(`✗ 风格检查未通过，共 ${problems.length} 处问题：\n`)
  for (const problem of problems) {
    console.error(`  ${problem.rel}:${problem.lineNo}  ${problem.reason}`)
    console.error(`    ${problem.text}`)
  }
  console.error('\n参考 github-showcase/github-style.md 的规则修正后重试。')
  process.exit(1)
}

console.log('✓ 风格检查通过：未发现违反 GitHub 风格约束的写法。')
