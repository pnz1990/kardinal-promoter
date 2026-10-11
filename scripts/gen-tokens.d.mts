// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Types for gen-tokens.mjs, imported by the web tests (src/tokens.test.ts).

export type TokenTree = { [key: string]: unknown }
export interface FlatToken {
  path: string[]
  token: { $value: unknown; $type?: string; $extensions?: { kardinal?: { contrast?: { on: string; min: number }[]; css?: boolean } } }
}
export const ROOT: string
export const TOKENS_FILE: string
export const WEB_CSS: string
export const WEB_TS: string
export const DOCS_CSS: string
export const WEB_FONTS: string
export const DOCS_FONTS: string
export const PUBLIC_FONTS: string
export function flatten(tree: TokenTree, prefix?: string[]): FlatToken[]
export function cssName(path: string[]): string
export function resolve(tokens: TokenTree, ref: string, theme: 'light' | 'dark'): unknown
export function render(tokens: TokenTree): Record<string, string>
export function fontCopies(root?: string): Record<string, string>
export function loadTokens(root?: string): TokenTree
