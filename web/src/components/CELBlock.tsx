// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/CELBlock.tsx — a CEL expression with syntax colours (#333), shown by the stage
// drawer's Details (they were the node details panel's).
import '../styles/NodeDetail.css'

type CELTokenType = 'keyword' | 'function' | 'string' | 'number' | 'operator' | 'boolean' | 'identifier' | 'plain'
interface CELToken { type: CELTokenType; text: string }

const CEL_KEYWORDS = new Set(['true', 'false', 'null', 'in', 'has', 'all', 'exists', 'map', 'filter', 'exists_one', 'type'])
const CEL_KRO_FUNCTIONS = new Set([
  'json.marshal', 'json.unmarshal', 'lists.setAtIndex',
  'lists.insertAtIndex', 'lists.removeAtIndex', 'random.seededInt', 'random.seededString',
  'changewindow.isAllowed', 'changewindow.isBlocked', 'lowerAscii', 'contains', 'startsWith', 'endsWith',
  'matches', 'size', 'int', 'uint', 'double', 'string', 'bytes', 'duration', 'timestamp',
])

export function tokenizeCEL(expr: string): CELToken[] {
  const tokens: CELToken[] = []
  let i = 0
  const len = expr.length

  while (i < len) {
    const ch = expr[i]

    // String literal: single or double quoted
    if (ch === '"' || ch === "'") {
      const quote = ch
      let j = i + 1
      while (j < len && expr[j] !== quote) {
        if (expr[j] === '\\') j++ // skip escape
        j++
      }
      j++ // closing quote
      tokens.push({ type: 'string', text: expr.slice(i, j) })
      i = j
      continue
    }

    // Number
    if (/[0-9]/.test(ch) || (ch === '-' && /[0-9]/.test(expr[i + 1] ?? ''))) {
      let j = i + 1
      while (j < len && /[0-9.]/.test(expr[j])) j++
      tokens.push({ type: 'number', text: expr.slice(i, j) })
      i = j
      continue
    }

    // Operator / punctuation
    if (/[!&|=<>+\-*/%()[\]{},.:@]/.test(ch)) {
      // Multi-char operators: !=, ==, <=, >=, &&, ||
      const two = expr.slice(i, i + 2)
      if (['!=', '==', '<=', '>=', '&&', '||'].includes(two)) {
        tokens.push({ type: 'operator', text: two })
        i += 2
        continue
      }
      tokens.push({ type: 'operator', text: ch })
      i++
      continue
    }

    // Whitespace
    if (/\s/.test(ch)) {
      let j = i + 1
      while (j < len && /\s/.test(expr[j])) j++
      tokens.push({ type: 'plain', text: expr.slice(i, j) })
      i = j
      continue
    }

    // Identifier or keyword
    if (/[a-zA-Z_$]/.test(ch)) {
      let j = i + 1
      while (j < len && /[a-zA-Z0-9_.[\]]/.test(expr[j])) j++
      const word = expr.slice(i, j)
      // check if it's a kro function call prefix (e.g. "json.unmarshal")
      let type: CELTokenType = 'identifier'
      if (CEL_KEYWORDS.has(word.toLowerCase())) {
        type = word === 'true' || word === 'false' ? 'boolean' : 'keyword'
      } else if (CEL_KRO_FUNCTIONS.has(word)) {
        type = 'function'
      } else if (/^[a-zA-Z_][a-zA-Z0-9_]*$/.test(word) && j < len && expr[j] === '(') {
        // Function call: word followed by (
        type = 'function'
      }
      tokens.push({ type, text: word })
      i = j
      continue
    }

    // Fallback
    tokens.push({ type: 'plain', text: ch })
    i++
  }
  return tokens
}


/** #333: Syntax-highlighted CEL expression block. */
export function CELBlock({ expression }: { expression: string }) {
  const tokens = tokenizeCEL(expression)
  return (
    <code className="cel-block">
      {tokens.map((token, idx) => (
        // The token type picks the syntax colour (NodeDetail.css, --k-syntax-*).
        <span key={idx} className={`cel-token cel-token--${token.type}`}>
          {token.text}
        </span>
      ))}
    </code>
  )
}
// ── end CEL syntax highlighter ────────────────────────────────────────────────
