// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// ThemeContext.tsx — React context for dark/light mode theming.
//
// Follows `prefers-color-scheme`, including OS changes while the tab is open, until
// the user picks a theme with `toggleTheme()`. Only that explicit choice is saved
// to localStorage (key `kardinal-theme`); from then on it wins over the OS.
// Applies `data-theme="light"` to `document.documentElement` for light mode;
// the default (no attribute) is dark.
import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

export type Theme = 'dark' | 'light'

const STORAGE_KEY = 'kardinal-theme'

interface ThemeContextValue {
  theme: Theme
  toggleTheme: () => void
}

export const ThemeContext = createContext<ThemeContextValue>({
  theme: 'dark',
  toggleTheme: () => {},
})

/** The theme the user picked explicitly, if any. */
function storedTheme(): Theme | undefined {
  const stored = localStorage.getItem(STORAGE_KEY)
  return stored === 'dark' || stored === 'light' ? stored : undefined
}

/** Returns the initial theme: localStorage preference, then system preference, then dark. */
function resolveInitialTheme(): Theme {
  if (typeof window === 'undefined') return 'dark'
  const stored = storedTheme()
  if (stored) return stored
  if (window.matchMedia('(prefers-color-scheme: light)').matches) return 'light'
  return 'dark'
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(resolveInitialTheme)

  useEffect(() => {
    // Apply or remove data-theme attribute to root element.
    const root = document.documentElement
    if (theme === 'light') {
      root.setAttribute('data-theme', 'light')
    } else {
      root.removeAttribute('data-theme')
    }
  }, [theme])

  // Listen for system preference changes (e.g., user changes OS theme while tab is open).
  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: light)')
    const handler = (e: MediaQueryListEvent) => {
      // Only follow system if user hasn't set an explicit preference.
      if (!storedTheme()) {
        setTheme(e.matches ? 'light' : 'dark')
      }
    }
    mq.addEventListener('change', handler)
    return () => mq.removeEventListener('change', handler)
  }, [])

  const toggleTheme = () => {
    const next: Theme = theme === 'dark' ? 'light' : 'dark'
    localStorage.setItem(STORAGE_KEY, next)
    setTheme(next)
  }

  return <ThemeContext.Provider value={{ theme, toggleTheme }}>{children}</ThemeContext.Provider>
}

/** Returns current theme and toggle function. */
export function useTheme(): ThemeContextValue {
  return useContext(ThemeContext)
}
