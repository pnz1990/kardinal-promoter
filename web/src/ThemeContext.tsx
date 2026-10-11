// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// ThemeContext.tsx — the theme (System / Light / Dark) and density (Comfortable / Compact).
//
// The theme follows `prefers-color-scheme`, including OS changes while the tab is open,
// until the user picks Light or Dark; picking System follows the OS again. Only an
// explicit Light or Dark is saved (localStorage `kardinal-theme`). `data-theme` on
// <html> is always the resolved theme ("light" or "dark"), so styles/tokens.css applies
// it; before the app runs, public/theme-boot.js sets the saved choice, and the OS
// setting applies through the CSS media query.
//
// Density is saved as `kardinal-density` and set as `data-density` on <html>; Compact
// scales the --k-space-* tokens.
import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

export type Theme = 'dark' | 'light'
export type ThemePreference = 'system' | Theme
export type Density = 'comfortable' | 'compact'

const THEME_KEY = 'kardinal-theme'
const DENSITY_KEY = 'kardinal-density'

/** The order the theme control cycles through. */
const NEXT_PREFERENCE: Record<ThemePreference, ThemePreference> = {
  system: 'light',
  light: 'dark',
  dark: 'system',
}

interface ThemeContextValue {
  /** The theme in effect. */
  theme: Theme
  /** What the user chose: System follows the OS. */
  preference: ThemePreference
  setPreference: (p: ThemePreference) => void
  /** Cycles System → Light → Dark → System. */
  toggleTheme: () => void
  density: Density
  setDensity: (d: Density) => void
}

export const ThemeContext = createContext<ThemeContextValue>({
  theme: 'light',
  preference: 'system',
  setPreference: () => {},
  toggleTheme: () => {},
  density: 'comfortable',
  setDensity: () => {},
})

function read(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function write(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key)
    else localStorage.setItem(key, value)
  } catch {
    // Storage blocked (private mode): the choice lasts for this page only.
  }
}

/** The theme the user picked explicitly, if any. */
function storedPreference(): ThemePreference {
  const stored = read(THEME_KEY)
  return stored === 'dark' || stored === 'light' ? stored : 'system'
}

function storedDensity(): Density {
  return read(DENSITY_KEY) === 'compact' ? 'compact' : 'comfortable'
}

/** The OS setting. Light unless the OS asks for dark, as styles/tokens.css does (light is
 * its default; dark comes from the prefers-color-scheme: dark media query). */
const DARK_QUERY = '(prefers-color-scheme: dark)'

function systemTheme(): Theme {
  if (typeof window === 'undefined' || !window.matchMedia) return 'light'
  return window.matchMedia(DARK_QUERY).matches ? 'dark' : 'light'
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [preference, setPreferenceState] = useState<ThemePreference>(storedPreference)
  const [system, setSystem] = useState<Theme>(systemTheme)
  const [density, setDensityState] = useState<Density>(storedDensity)
  const theme: Theme = preference === 'system' ? system : preference

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
  }, [theme])

  useEffect(() => {
    document.documentElement.setAttribute('data-density', density)
  }, [density])

  // Follow OS changes while the tab is open (used while the preference is System).
  useEffect(() => {
    if (!window.matchMedia) return
    const mq = window.matchMedia(DARK_QUERY)
    const handler = (e: MediaQueryListEvent) => setSystem(e.matches ? 'dark' : 'light')
    mq.addEventListener('change', handler)
    return () => mq.removeEventListener('change', handler)
  }, [])

  const setPreference = (p: ThemePreference) => {
    write(THEME_KEY, p === 'system' ? null : p)
    setPreferenceState(p)
  }
  const toggleTheme = () => setPreference(NEXT_PREFERENCE[preference])
  const setDensity = (d: Density) => {
    write(DENSITY_KEY, d === 'comfortable' ? null : d)
    setDensityState(d)
  }

  return (
    <ThemeContext.Provider value={{ theme, preference, setPreference, toggleTheme, density, setDensity }}>
      {children}
    </ThemeContext.Provider>
  )
}

/** Returns the theme, the user's preference and density, and their setters. */
export function useTheme(): ThemeContextValue {
  return useContext(ThemeContext)
}

const PREFERENCE_LABEL: Record<ThemePreference, string> = { system: 'System', light: 'Light', dark: 'Dark' }

/** The accessible name of the theme control: what it is set to, and what a click does. */
export function themeControlLabel(preference: ThemePreference, theme: Theme): string {
  const now = preference === 'system' ? `System (${theme})` : PREFERENCE_LABEL[preference]
  return `Theme: ${now}. Switch to ${PREFERENCE_LABEL[NEXT_PREFERENCE[preference]]}`
}
