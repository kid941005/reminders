# Example apps

This directory holds sample `apps.App` implementations used by tests as
reference integrations. They are deliberately **not** blank-imported by
`main.go`, so the production binary contains only the modules registered under
`server/apps` plus real features.

- `qrcode/` — registers `/api/qrcode` and seeds a demo config row; used by
  `server/server_test.go` to exercise the generic app registry.
