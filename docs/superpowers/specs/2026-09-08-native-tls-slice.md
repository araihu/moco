# Native TLS

Optional HTTPS uses local PEM certificate and key files configured together
through MOCO_TLS_CERT_FILE and MOCO_TLS_KEY_FILE. Startup loads the matching pair
before database migrations or serving traffic. Invalid configuration fails
closed; there is no HTTP fallback when TLS is requested.

TLS 1.2 is the minimum supported protocol. Public API, internal API and probes
share the HTTPS listener. Existing bearer authentication and authorization apply.
Without either variable, the existing HTTP listener remains available.

Certificates are loaded at startup; replacement requires restart. Issuance,
renewal and trust distribution are deployment responsibilities. No external
service or Go module dependency is added. API payloads and routes are unchanged.
