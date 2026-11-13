# Ramedia Server

Ramedia Server is the Ramindex-maintained fork of Silo. Its web client, default
server name, installed web app, bundled icons, setup screens and help copy use
Ramedia Server branding. Uploaded custom server names and brand assets still
take precedence. Existing default names saved as `Silo` are updated on upgrade.

The fork retains the upstream license and attribution. Silo remains the name of
the upstream project and its native apps, plugin SDK, push relay and shared
marker service. Those references describe the actual products and services.

## Compatibility

This is a presentation change. The `silo-server` repository and `silo` executable,
Go module paths, `/api/v1` and `/api/v2` routes, `X-Silo-*` headers, storage keys,
plugin IDs and Live TV contracts keep their existing identities. Native clients
are unchanged and may connect using the same server address and credentials.

Dispatcharr, XC and HDHomeRun remain independent of the optional premiere API.
Their normal channel, guide and playback routes work on the previous host
without the premiere modification. Ramedia Server exposes the web reminder
controls when `/api/v2/premieres/capabilities` reports availability.

## Deployment

The deployment branch is built on the existing server runtime, with an image-only
Compose override. Keep the active overrides, database, Redis, mounts and GPU
configuration when upgrading. The branding migration changes only the legacy
default server label; it preserves custom names, account IDs and data.
