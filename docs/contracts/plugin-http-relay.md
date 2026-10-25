# Local streaming relay for plugin HTTP routes

This is a Ramindex fork extension. It lets an authenticated plugin route deliver an unbounded media stream without buffering it in the SDK's `HandleHTTPResponse.Body`.

A trusted installed plugin can return status `200` and a header:

```text
X-Silo-Relay-URL: http://127.0.0.1:49152/opaque-local-resource
```

Silo consumes that header internally. It admits only HTTP URLs with a literal loopback address, explicit port, no userinfo, and no fragment. The declared route must require `authenticated` or `admin` access; the request must be GET or HEAD and satisfy normal route authentication/authorization. Public route responses cannot use this extension. Redirects are refused. This is a trusted-plugin response field, never a caller-supplied URL parameter.

The plugin must bind its listener to loopback and authorize the requested resource using the trusted caller headers before returning the relay target. `X-Silo-User-Id`, `X-Silo-User-Role`, and the validated `X-Silo-Profile-Id` come from Silo's access context; caller-supplied `X-Silo-*` headers are not forwarded. A plugin serving session media should check both account and profile ownership on every resource request.

Silo forwards only the method, Range, and cancellation context. Bearer credentials, cookies, and other caller headers never reach the loopback producer. Content-Type, Content-Length, Content-Range, and Accept-Ranges may return to the browser; Set-Cookie and the internal relay URL do not. Responses use `no-store` and `nosniff`. Bytes flush as they arrive; cancellation closes the upstream request. Initial response headers have a 15-second timeout and the relay imposes no whole-stream deadline.

Dynamic plugin responses advertise `X-Silo-Stream-Relay: 1`. A plugin UI can detect this header to explain that browser streaming requires a newer fork when running on an older host. The header does not grant access or modify route permissions.

The plugin process and the serving Silo instance must share the listener's network namespace. A gateway URL and any in-memory reservations are local to that process; they are not cluster coordination. Load balancing or remote plugin runtimes require an explicit affinity/ownership design before using this extension across nodes.

HLS producers must rewrite every media/key URI into the authenticated plugin route namespace. Returning an unmodified playlist would send the browser outside Silo's authentication boundary. The HDHomeRun plugin uses opaque resource IDs and relative URIs for this purpose.
