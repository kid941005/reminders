# Third-party notices

This project includes code derived from the following MIT-licensed project:

## tarekbecker/icloud-reminders-cli

- Upstream: https://github.com/tarekbecker/icloud-reminders-cli
- License: MIT (see `server/reminder/applesrp/LICENSE`)
- Used for: the SRP client implementation under `server/reminder/applesrp/`, and
  the CloudKit reminder record/title encoding approach implemented in
  `server/reminder/apple_cloudkit_title.go`, `apple_cloudkit_auth.go`, and
  `apple_cloudkit_client.go`. The code has been adapted to this project's
  package layout, error handling and data model.
