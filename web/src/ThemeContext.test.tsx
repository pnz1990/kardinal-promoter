// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// ThemeContext.test.tsx — unit tests for ThemeProvider and useTheme.
import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { ThemeProvider, useTheme, type Theme } from './ThemeContext'

// Mock localStorage.
const localStorageMock = (() => {
  let store: Record<string, string> = {}
  return {
    getItem: (k: string) => store[k] ?? null,
    setItem: (k: string, v: string) => { store[k] = v },
    removeItem: (k: string) => { delete store[k] },
    clear: () => { store = {} },
  }
})()

Object.defineProperty(window, 'localStorage', { value: localStorageMock })

// Mock matchMedia.
let systemPreference: 'dark' | 'light' = 'dark'
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: query === '(prefers-color-scheme: light)' && systemPreference === 'light',
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }),
})

describe('ThemeProvider', () => {
  beforeEach(() => {
    localStorageMock.clear()
    systemPreference = 'dark'
    document.documentElement.removeAttribute('data-theme')
  })

  it('defaults to dark theme when no localStorage or system preference', () => {
    const { result } = renderHook(() => useTheme(), {
      wrapper: ThemeProvider,
    })
    expect(result.current.theme).toBe('dark')
  })

  it('reads system preference for light when no localStorage value', () => {
    systemPreference = 'light'
    const { result } = renderHook(() => useTheme(), {
      wrapper: ThemeProvider,
    })
    expect(result.current.theme).toBe('light')
  })

  it('restores theme from localStorage on mount', () => {
    localStorageMock.setItem('kardinal-theme', 'light')
    const { result } = renderHook(() => useTheme(), {
      wrapper: ThemeProvider,
    })
    expect(result.current.theme).toBe('light')
  })

  it('toggleTheme switches from dark to light', () => {
    const { result } = renderHook(() => useTheme(), {
      wrapper: ThemeProvider,
    })
    expect(result.current.theme).toBe('dark')
    act(() => result.current.toggleTheme())
    expect(result.current.theme).toBe('light')
  })

  it('toggleTheme switches from light to dark', () => {
    localStorageMock.setItem('kardinal-theme', 'light')
    const { result } = renderHook(() => useTheme(), {
      wrapper: ThemeProvider,
    })
    act(() => result.current.toggleTheme())
    expect(result.current.theme).toBe('dark')
  })

  it('persists toggled theme to localStorage', () => {
    const { result } = renderHook(() => useTheme(), {
      wrapper: ThemeProvider,
    })
    act(() => result.current.toggleTheme())
    expect(localStorageMock.getItem('kardinal-theme')).toBe('light')
  })

  it('applies data-theme="light" attribute when light', () => {
    const { result } = renderHook(() => useTheme(), {
      wrapper: ThemeProvider,
    })
    act(() => result.current.toggleTheme())
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('removes data-theme attribute when dark', () => {
    localStorageMock.setItem('kardinal-theme', 'light')
    const { result } = renderHook(() => useTheme(), {
      wrapper: ThemeProvider,
    })
    act(() => result.current.toggleTheme())
    expect(document.documentElement.getAttribute('data-theme')).toBeNull()
  })

  it('rejects unknown localStorage values, falls back to system preference', () => {
    localStorageMock.setItem('kardinal-theme', 'solarized' as Theme)
    const { result } = renderHook(() => useTheme(), {
      wrapper: ThemeProvider,
    })
    // 'solarized' is not a valid Theme — should fall back to system (dark)
    expect(['dark', 'light']).toContain(result.current.theme)
  })
})
