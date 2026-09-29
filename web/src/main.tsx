// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { ThemeProvider } from './ThemeContext'
import { TokenPrompt } from './components/TokenPrompt'
import './theme.css'

const root = document.getElementById('root')
if (!root) throw new Error('Root element not found')

createRoot(root).render(
  <StrictMode>
    <ThemeProvider>
      <App />
      <TokenPrompt />
    </ThemeProvider>
  </StrictMode>,
)
