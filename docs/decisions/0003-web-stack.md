# 0003 — React + Vite for the web app

**Status:** accepted
**Date:** 2026-10-30

## Context

The first client is a web app for task inboxes, rooms, approvals, agent status, and audit timelines. It must be fast to develop, accessible, and reusable inside future desktop and mobile shells. The app is authenticated and realtime; it does not need SEO or server-side rendering.

## Decision

Use TypeScript, React, Vite, React Router, TanStack Query, Tailwind CSS, Radix primitives, and shadcn/ui. The app is a static SPA served by the Go server or a CDN.

## Consequences

- A large ecosystem of components, testing tools, and developers.
- Vite keeps the development loop fast; the build is a static bundle.
- The same build can be wrapped by Tauri for desktop and Capacitor for mobile.
- The API client is generated from OpenAPI, so wire types are not hand-written.
- The app must handle its own routing, authentication state, and error boundaries; there is no server-rendering framework doing that.
- Accessibility depends on using Radix primitives correctly and testing keyboard flows.

## Alternatives considered

- **Next.js:** excellent for content and SSR, but adds a server runtime and caching model that this authenticated dashboard does not need.
- **Vue/Svelte:** both are good; React was chosen for ecosystem size and the availability of accessible primitives.
- **Server-rendered Go templates:** simpler deployment, but a poorer fit for a rich realtime app and future shells.
- **React Native for mobile:** would create a second UI codebase; Capacitor is the default until native capabilities require otherwise.

## Review trigger

Revisit if the web app cannot meet a concrete accessibility or performance requirement, or if a future shell cannot reuse the web build.
