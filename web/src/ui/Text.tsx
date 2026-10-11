// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Text.tsx — code, keys and layout: CodeInline, CodeBlock, Kbd, Stack.
import { forwardRef, type HTMLAttributes, type ReactNode } from 'react'
import CopyButton from '../components/CopyButton'
import { cx } from './cx'

export function CodeInline({ className, ...rest }: HTMLAttributes<HTMLElement>) {
  return <code className={cx('k-code', className)} {...rest} />
}

export interface CodeBlockProps extends HTMLAttributes<HTMLPreElement> {
  language?: 'cel' | 'yaml' | 'shell' | 'json' | 'text'
  /** Shows a copy button for the block's text. */
  copy?: boolean
  children: string
}

export function CodeBlock({ language = 'text', copy, className, children, ...rest }: CodeBlockProps) {
  return (
    <div className="k-codeblock">
      <pre className={cx('k-codeblock__pre', className)} data-language={language} {...rest}>
        <code>{children}</code>
      </pre>
      {copy && <CopyButton value={children} label={`Copy ${language === 'text' ? 'text' : language}`} />}
    </div>
  )
}

export function Kbd({ className, ...rest }: HTMLAttributes<HTMLElement>) {
  return <kbd className={cx('k-kbd', className)} {...rest} />
}

export type Space = '0' | '0_5' | '1' | '1_5' | '2' | '3' | '4' | '6' | '8'

export interface StackProps extends HTMLAttributes<HTMLDivElement> {
  /** --k-space-N between the children. */
  gap?: Space
  direction?: 'row' | 'column'
  align?: 'start' | 'center' | 'end' | 'baseline' | 'stretch'
  justify?: 'start' | 'center' | 'end' | 'between'
  wrap?: boolean
  as?: 'div' | 'section' | 'header' | 'footer' | 'ul' | 'ol' | 'nav'
  children?: ReactNode
}

export const Stack = forwardRef<HTMLDivElement, StackProps>(function Stack(
  { gap = '2', direction = 'column', align, justify, wrap, as: As = 'div', className, ...rest }, ref,
) {
  return (
    <As
      ref={ref as never}
      className={cx(
        'k-stack', `k-stack--${direction}`, `k-stack--gap-${gap}`,
        align && `k-stack--align-${align}`, justify && `k-stack--justify-${justify}`, wrap && 'k-stack--wrap', className,
      )}
      {...rest}
    />
  )
})
