# Client applications

Each user-facing application has its own directory and build configuration:

- [android](android/): native Kotlin/Jetpack Compose cabinet.
- [web](web/): responsive website and personal cabinet.

Additional clients, such as iOS, belong in their own sibling directories. Clients
communicate with the same backend through versioned HTTP APIs defined under
[`contracts`](../contracts/). They must not access SQLite, MediaMTX management,
Node Agents or worker credentials directly.

Keep platform-specific UI, session storage and build tooling inside each application.
Share API contracts first; extract reusable client code only when there is working
code used by multiple clients. Android and the website use the same user API with separate native and browser interfaces.
