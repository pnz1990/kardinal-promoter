// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// ThemeContext.test.tsx — unit tests for ThemeProvider and useTheme.
import { renderHook, act } from '@testing-library/react'
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { ThemeProvider, useTheme, themeControlLabel, type Theme } from './ThemeContext'

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
// 'none': the browser reports no preference (neither query matches).
let systemPreference: 'dark' | 'light' | 'none' = 'dark'
Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: query === `(prefers-color-scheme: ${systemPreference})`,
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

  it('follows a dark OS setting when nothing is saved', () => {
    const { result } = renderHook(() => useTheme(), {
      wrapper: ThemeProvider,
    })
    expect(result.current.theme).toBe('dark')
  })

  it('is light when the OS reports no preference, as the CSS is', () => {
    systemPreference = 'none'
    const { result } = renderHook(() => useTheme(), { wrapper: ThemeProvider })
    expect(result.current.theme).toBe('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
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

  it('applies data-theme="dark" when dark', () => {
    localStorageMock.setItem('kardinal-theme', 'light')
    const { result } = renderHook(() => useTheme(), {
      wrapper: ThemeProvider,
    })
    act(() => result.current.toggleTheme())
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('cycles System → Light → Dark → System, saving only Light and Dark', () => {
    systemPreference = 'light'
    const { result } = renderHook(() => useTheme(), { wrapper: ThemeProvider })
    expect(result.current.preference).toBe('system')
    expect(result.current.theme).toBe('light')
    act(() => result.current.toggleTheme())
    expect(result.current.preference).toBe('light')
    expect(localStorageMock.getItem('kardinal-theme')).toBe('light')
    act(() => result.current.toggleTheme())
    expect(result.current.preference).toBe('dark')
    expect(result.current.theme).toBe('dark')
    expect(localStorageMock.getItem('kardinal-theme')).toBe('dark')
    act(() => result.current.toggleTheme())
    expect(result.current.preference).toBe('system')
    expect(result.current.theme).toBe('light')
    expect(localStorageMock.getItem('kardinal-theme')).toBeNull()
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('names the control by its setting and what a click does', () => {
    expect(themeControlLabel('system', 'dark')).toBe('Theme: System (dark). Switch to Light')
    expect(themeControlLabel('light', 'light')).toBe('Theme: Light. Switch to Dark')
    expect(themeControlLabel('dark', 'dark')).toBe('Theme: Dark. Switch to System')
  })

  it.each([
    { os: 'dark' as const, want: 'dark' },
    { os: 'light' as const, want: 'light' },
  ])('ignores an unknown saved value and follows the OS setting ($os)', ({ os, want }) => {
    systemPreference = os
    localStorageMock.setItem('kardinal-theme', 'solarized' as Theme)
    const { result } = renderHook(() => useTheme(), {
      wrapper: ThemeProvider,
    })
    expect(result.current.theme).toBe(want)
  })
})

describe('ThemeProvider — density', () => {
  beforeEach(() => {
    localStorageMock.clear()
    document.documentElement.removeAttribute('data-density')
  })

  it('defaults to comfortable and sets data-density', () => {
    const { result } = renderHook(() => useTheme(), { wrapper: ThemeProvider })
    expect(result.current.density).toBe('comfortable')
    expect(document.documentElement.getAttribute('data-density')).toBe('comfortable')
  })

  it('saves compact and restores it', () => {
    const first = renderHook(() => useTheme(), { wrapper: ThemeProvider })
    act(() => first.result.current.setDensity('compact'))
    expect(document.documentElement.getAttribute('data-density')).toBe('compact')
    expect(localStorageMock.getItem('kardinal-density')).toBe('compact')
    first.unmount()
    const again = renderHook(() => useTheme(), { wrapper: ThemeProvider })
    expect(again.result.current.density).toBe('compact')
    act(() => again.result.current.setDensity('comfortable'))
    expect(localStorageMock.getItem('kardinal-density')).toBeNull()
  })

})

// Audit C10b-web-20: the theme follows the OS until the user picks one.
describe('ThemeProvider — following the OS setting', () => {
  let osChange: ((e: MediaQueryListEvent) => void) | undefined
  const realMatchMedia = window.matchMedia

  beforeEach(() => {
    localStorageMock.clear()
    systemPreference = 'dark'
    osChange = undefined
    window.matchMedia = ((query: string) => ({
      ...realMatchMedia(query),
      addEventListener: (_: string, h: (e: MediaQueryListEvent) => void) => { osChange = h },
    })) as typeof window.matchMedia
  })
  afterEach(() => { window.matchMedia = realMatchMedia })

  it('does not save a theme the user never picked', () => {
    renderHook(() => useTheme(), { wrapper: ThemeProvider })
    expect(localStorageMock.getItem('kardinal-theme')).toBeNull()
  })

  it.each([
    { name: 'follows an OS switch to light when nothing is saved', stored: undefined, want: 'light' },
    { name: 'follows an OS switch when the saved value is not a theme', stored: 'solarized', want: 'light' },
    { name: 'keeps the theme the user picked when the OS switches', stored: 'dark', want: 'dark' },
  ])('$name', ({ stored, want }) => {
    if (stored) localStorageMock.setItem('kardinal-theme', stored)
    const { result } = renderHook(() => useTheme(), { wrapper: ThemeProvider })
    act(() => osChange?.({ matches: false } as MediaQueryListEvent)) // the dark query stops matching
    expect(result.current.theme).toBe(want)
  })

  it('follows a change event of the dark-scheme query, and stops listening on unmount', () => {
    window.matchMedia = realMatchMedia
    systemPreference = 'light'
    // A MediaQueryList the test can fire: an EventTarget per query.
    const lists = new Map<string, EventTarget & { matches: boolean; media: string }>()
    const original = window.matchMedia
    window.matchMedia = ((query: string) => {
      let mql = lists.get(query)
      if (!mql) {
        mql = Object.assign(new EventTarget(), { matches: query === '(prefers-color-scheme: light)', media: query })
        lists.set(query, mql)
      }
      return mql as unknown as MediaQueryList
    }) as typeof window.matchMedia
    try {
      const { result, unmount } = renderHook(() => useTheme(), { wrapper: ThemeProvider })
      expect(result.current.theme).toBe('light')
      const dark = lists.get('(prefers-color-scheme: dark)')
      expect(dark, 'the provider listens to the dark-scheme query').toBeDefined()
      act(() => {
        dark!.matches = true
        dark!.dispatchEvent(Object.assign(new Event('change'), { matches: true }))
      })
      expect(result.current.theme).toBe('dark')
      expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
      act(() => { dark!.dispatchEvent(Object.assign(new Event('change'), { matches: false })) })
      expect(result.current.theme).toBe('light')
      unmount()
      act(() => { dark!.dispatchEvent(Object.assign(new Event('change'), { matches: true })) })
    } finally {
      window.matchMedia = original
    }
  })
})
