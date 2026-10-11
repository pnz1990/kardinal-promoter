// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// The UI primitives (kardinal-parity/UI-PRIMITIVES.md). Styled by ui.css with --k-* tokens.
import './ui.css'

export { Button, IconButton, type ButtonProps, type ButtonVariant, type ButtonSize, type IconButtonProps } from './Button'
export { StatusChip, Badge, Tag, type StatusChipProps, type BadgeProps, type TagProps, type TagKind } from './Chips'
export { Dialog, ConfirmDialog, Drawer, type DialogProps, type ConfirmDialogProps, type DrawerProps } from './Dialog'
export { Tabs, TabPanel, tabId, panelId, type TabsProps, type TabPanelProps, type TabItem } from './Tabs'
export { MenuButton, type MenuButtonProps } from './MenuButton'
export { Icon, ICON_NAMES, type IconName, type IconProps } from './Icon'
export { ErrorNotice, type ErrorNoticeProps } from './ErrorNotice'
export { KubernetesDetails, type KubernetesDetailsProps } from './KubernetesDetails'
export { CodeInline, CodeBlock, Kbd, Stack, type CodeBlockProps, type StackProps, type Space } from './Text'
export { TONES, toneVars, type Tone } from './tone'
export { cx } from './cx'
export { Skeleton, type SkeletonProps } from './Skeleton'
