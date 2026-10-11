// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// NotFound.tsx — a route that names a pipeline or a version the cluster does not have. The
// page says so and links back; it never shows another pipeline or version in its place.
import '../styles/NotFound.css'

interface Props {
  title: string
  detail: string
  /** Where the link goes (a #/… route). */
  href: string
  linkText: string
}

export function NotFound({ title, detail, href, linkText }: Props) {
  return (
    <section className="not-found" aria-labelledby="not-found-title">
      <h2 id="not-found-title" className="not-found__title">{title}</h2>
      <p className="not-found__detail">{detail}</p>
      <a className="not-found__link" href={href}>{linkText}</a>
    </section>
  )
}
